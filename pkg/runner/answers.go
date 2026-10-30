package runner

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
	runnerv1 "github.com/ocelhq/ocel/pkg/proto/runner/v1"
)

func (s *Server) AnswerConfirmation(ctx context.Context, request *runnerv1.AnswerConfirmationRequest) (*runnerv1.AnswerConfirmationResponse, error) {
	caller, err := s.person(ctx)
	if err != nil {
		return nil, err
	}
	key := jobKey{caller.OrganizationID, request.GetJobId()}
	if err := s.refuseRoleBelowCommand(caller, key); err != nil {
		return nil, err
	}
	answer := &streamv1.InputAnswer{InputId: request.GetInputId(), Answer: &streamv1.InputAnswer_Confirmed{Confirmed: request.GetConfirmed()}}
	job, err := s.answer(key, request.GetInputId(), false, answer, func(input *runnerv1.JobInput) (string, error) {
		confirmation := input.GetConfirmation()
		if confirmation == nil {
			return "", connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("input %s asks for values, not a confirmation", input.GetId()))
		}
		if request.GetConfirmed() {
			return answeredBy(caller.Actor, "yes") + confirmation.GetQuestion(), nil
		}
		return answeredBy(caller.Actor, "no") + confirmation.GetQuestion(), nil
	})
	if err != nil {
		return nil, err
	}
	return &runnerv1.AnswerConfirmationResponse{Job: job}, nil
}

func answeredBy(actor, answer string) string {
	if actor == "" {
		return "Answered " + answer + ": "
	}
	return actor + " answered " + answer + ": "
}

func (s *Server) SupplyVariables(ctx context.Context, request *runnerv1.SupplyVariablesRequest) (*runnerv1.SupplyVariablesResponse, error) {
	caller, err := s.person(ctx)
	if err != nil {
		return nil, err
	}
	key := jobKey{caller.OrganizationID, request.GetJobId()}
	if err := s.refuseRoleBelowCommand(caller, key); err != nil {
		return nil, err
	}
	inputID := request.GetInputId()
	values := request.GetVariables().GetValues()

	var stored VariableValues
	if _, err := s.change(key, func(h *heldJob) (bool, error) {
		if err := awaitsAnswer(h, inputID); err != nil {
			return false, err
		}
		needed := h.job.GetInput().GetVariables()
		if needed == nil {
			return false, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("input %s asks a question, not for values", inputID))
		}
		if err := refuseUnaskedValues(inputID, needed, values); err != nil {
			return false, err
		}
		if s.storeVariables == nil {
			return false, connect.NewError(connect.CodeFailedPrecondition, errors.New("this runner stores no variables: set them with `ocel env set`, then start the job again"))
		}
		stored = VariableValues{OrganizationID: h.record.Organization, ProjectID: h.record.Token.ProjectID, Slug: h.record.Token.Slug, Tier: needed.GetTier(), Values: values}
		h.record.Answering = inputID
		return true, nil
	}); err != nil {
		return nil, err
	}
	release := func() {
		_, _ = s.change(key, func(h *heldJob) (bool, error) {
			if h.record.Answering != inputID {
				return false, nil
			}
			h.record.Answering = ""
			return true, nil
		})
	}
	if err := s.storeVariables(ctx, stored); err != nil {
		release()
		return nil, fmt.Errorf("store the values: %w", err)
	}
	names := make([]string, 0, len(values))
	for _, value := range values {
		names = append(names, cellName(value.GetFolder(), value.GetKey()))
	}
	answer := &streamv1.InputAnswer{InputId: inputID, Answer: &streamv1.InputAnswer_Resolved{Resolved: true}}
	job, err := s.answer(key, inputID, true, answer, func(*runnerv1.JobInput) (string, error) {
		if caller.Actor == "" {
			return "Values given for " + strings.Join(names, ", "), nil
		}
		return "Values given by " + caller.Actor + " for " + strings.Join(names, ", "), nil
	})
	if err != nil {
		release()
		return nil, err
	}
	return &runnerv1.SupplyVariablesResponse{Job: job}, nil
}

func (s *Server) answer(key jobKey, inputID string, storing bool, answer *streamv1.InputAnswer, describe func(input *runnerv1.JobInput) (string, error)) (*runnerv1.Job, error) {
	line, err := AnswerLine(answer)
	if err != nil {
		return nil, err
	}
	h, err := s.change(key, func(h *heldJob) (bool, error) {
		answering := h.record.Answering
		if storing && answering == inputID {
			answering = ""
		}
		if err := awaitsAnswerTo(h, inputID, answering); err != nil {
			return false, err
		}
		said, err := describe(h.job.GetInput())
		if err != nil {
			return false, err
		}
		h.record.Answering = ""
		h.record.Answers = append(h.record.Answers, string(line))
		h.job.State = runnerv1.JobState_JOB_STATE_RUNNING
		h.job.Input = nil
		h.job.Step = "Resuming with the answer"
		h.event(s.now(), Progress{Level: runnerv1.JobEventLevel_JOB_EVENT_LEVEL_INFO, Message: said})
		return true, nil
	})
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return proto.CloneOf(h.job), nil
}

func awaitsAnswer(h *heldJob, inputID string) error {
	return awaitsAnswerTo(h, inputID, h.record.Answering)
}

func awaitsAnswerTo(h *heldJob, inputID, answering string) error {
	job := h.job
	input := job.GetInput()
	switch {
	case job.GetState() != runnerv1.JobState_JOB_STATE_INPUT_REQUIRED || input == nil || h.record.CancelRequested:
		return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("job %s waits on no input: it is %s", job.GetId(), stateName(job.GetState())))
	case input.GetId() != inputID:
		return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("job %s waits on input %s, not %s: read the job again and answer what it asks now", job.GetId(), input.GetId(), inputID))
	case answering == inputID:
		return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("input %s of job %s is already being answered", inputID, job.GetId()))
	}
	return nil
}

func refuseUnaskedValues(inputID string, needed *runnerv1.VariablesNeeded, values []*runnerv1.VariableValue) error {
	wanted := map[string]bool{}
	for _, variable := range needed.GetVariables() {
		wanted[cellName(variable.GetFolder(), variable.GetKey())] = true
	}
	seen := map[string]bool{}
	var extra []string
	for _, value := range values {
		name := cellName(value.GetFolder(), value.GetKey())
		if seen[name] {
			return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("%s is given twice", name))
		}
		seen[name] = true
		if !wanted[name] {
			extra = append(extra, name)
		}
	}
	if len(extra) > 0 {
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("input %s asks for no %s: give values only for the variables it needs", inputID, strings.Join(extra, ", ")))
	}
	return nil
}

func cellName(folder, key string) string {
	if folder == "" {
		return key
	}
	return strings.TrimPrefix(folder, "/") + "/" + key
}

func stateName(state runnerv1.JobState) string {
	return strings.ToLower(strings.TrimPrefix(state.String(), "JOB_STATE_"))
}
