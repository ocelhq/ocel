package runner

import (
	"cmp"
	"context"
	"fmt"
	"strings"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/ocelhq/ocel/pkg/endpoint"
	"github.com/ocelhq/ocel/pkg/naming"
	runnerv1 "github.com/ocelhq/ocel/pkg/proto/runner/v1"
)

type jobAgentService struct{ s *Server }

func (a *jobAgentService) agentJob(ctx context.Context) (endpoint.Caller, jobKey, error) {
	caller, job, err := a.s.jobCaller(ctx)
	if err != nil {
		return endpoint.Caller{}, jobKey{}, err
	}
	return caller, jobKey{caller.OrganizationID, job.ID}, nil
}

func (a *jobAgentService) GetSpec(ctx context.Context, _ *runnerv1.GetSpecRequest) (*runnerv1.GetSpecResponse, error) {
	s := a.s
	_, key, err := a.agentJob(ctx)
	if err != nil {
		return nil, err
	}
	h, err := s.change(key, func(h *heldJob) (bool, error) {
		if ended(h.job.GetState()) {
			return false, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("job %s has ended", key.id))
		}
		h.start(s.now())
		return true, nil
	})
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	spec := &runnerv1.GetSpecResponse{
		JobId: h.job.GetId(), ProjectId: h.job.GetProjectId(), Tier: h.job.GetTier(),
		DeploymentId: h.job.GetDeploymentId(), Slug: h.record.Token.Slug, ConsoleUrl: h.record.Console,
		Job: proto.CloneOf(h.request),
	}
	expires := h.record.Token.Expires
	s.mu.Unlock()

	id := spec.GetJob().GetSourceId()
	if id == "" {
		return spec, nil
	}
	manifest, err := s.source(key.organization, id)
	if err != nil {
		return nil, err
	}
	life := max(expires.Sub(s.now()), time.Minute)
	archive := &runnerv1.SourceArchive{Manifest: manifest}
	signed := map[string]bool{}
	for _, file := range manifest.GetFiles() {
		digest := file.GetDigest()
		if digest == "" || signed[digest] {
			continue
		}
		signed[digest] = true
		archive.Blobs = append(archive.Blobs, &runnerv1.SourceBlob{Digest: digest, Url: s.blobs.sign("GET", key.organization, digest, life)})
	}
	spec.Archive = archive
	return spec, nil
}

func (a *jobAgentService) AppendOutput(ctx context.Context, request *runnerv1.AppendOutputRequest) (*runnerv1.AppendOutputResponse, error) {
	s := a.s
	_, key, err := a.agentJob(ctx)
	if err != nil {
		return nil, err
	}
	h, err := s.change(key, func(h *heldJob) (bool, error) {
		if ended(h.job.GetState()) {
			return false, nil
		}
		if request.GetOffset() > h.record.Lines || request.GetStderrOffset() > h.record.StderrLines {
			return false, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("the output starts at line %d and stderr line %d, past the %d and %d taken", request.GetOffset(), request.GetStderrOffset(), h.record.Lines, h.record.StderrLines))
		}
		summary, err := h.summary()
		if err != nil {
			return false, err
		}
		now := s.now()
		h.start(now)
		h.forgetGitToken()
		stream := ResumeStream(summary)
		for _, line := range request.GetLines()[min(h.record.Lines-request.GetOffset(), int64(len(request.GetLines()))):] {
			if progress, ok := stream.Line([]byte(line)); ok {
				h.apply(now, progress)
			}
			h.record.Lines++
		}
		if summary := stream.Summary(); summary != nil {
			if h.record.Summary, err = protojson.Marshal(summary); err != nil {
				return false, err
			}
		}
		for _, line := range request.GetStderr()[min(h.record.StderrLines-request.GetStderrOffset(), int64(len(request.GetStderr()))):] {
			h.record.StderrLines++
			text := strings.TrimRight(line, "\r\n")
			if text == "" {
				continue
			}
			h.record.StderrTail = append(h.record.StderrTail, text)
			if len(h.record.StderrTail) > stderrTail {
				h.record.StderrTail = h.record.StderrTail[1:]
			}
			if h.record.StderrLines <= maxStderrLines {
				h.event(now, Progress{Level: runnerv1.JobEventLevel_JOB_EVENT_LEVEL_DEBUG, Message: Clip(text)})
			}
		}
		return true, nil
	})
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return &runnerv1.AppendOutputResponse{Cancel: h.record.CancelRequested || ended(h.job.GetState())}, nil
}

func (a *jobAgentService) AwaitAnswer(ctx context.Context, request *runnerv1.AwaitAnswerRequest) (*runnerv1.AwaitAnswerResponse, error) {
	s := a.s
	_, key, err := a.agentJob(ctx)
	if err != nil {
		return nil, err
	}
	taken := int(request.GetTaken())
	wait := time.NewTimer(answerWait)
	defer wait.Stop()
	for {
		s.mu.Lock()
		h, ok := s.jobs[key]
		if !ok {
			s.mu.Unlock()
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("this runner holds no job %s", key.id))
		}
		answers, changed, over := h.record.Answers, h.changed, ended(h.job.GetState())
		s.mu.Unlock()
		if len(answers) > taken {
			return &runnerv1.AwaitAnswerResponse{Line: answers[taken]}, nil
		}
		if over {
			return &runnerv1.AwaitAnswerResponse{}, nil
		}
		select {
		case <-changed:
		case <-wait.C:
			return &runnerv1.AwaitAnswerResponse{}, nil
		case <-s.done:
			return &runnerv1.AwaitAnswerResponse{}, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

func (a *jobAgentService) Finish(ctx context.Context, request *runnerv1.FinishRequest) (*runnerv1.FinishResponse, error) {
	s := a.s
	_, key, err := a.agentJob(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := s.change(key, func(h *heldJob) (bool, error) {
		if ended(h.job.GetState()) {
			return false, nil
		}
		outcome, err := outcomeOf(h, request)
		if err != nil {
			return false, err
		}
		now := s.now()
		if outcome.Headline != "" {
			h.job.Step = outcome.Headline
		}
		switch {
		case outcome.Succeeded:
			h.job.Result = outcome.Result
			h.end(now, runnerv1.JobState_JOB_STATE_SUCCEEDED, nil)
		case h.record.CancelRequested:
			h.end(now, runnerv1.JobState_JOB_STATE_CANCELED, nil)
		default:
			h.job.Result = outcome.Result
			h.end(now, runnerv1.JobState_JOB_STATE_FAILED, outcome.Error)
		}
		return true, nil
	}); err != nil {
		return nil, err
	}
	return &runnerv1.FinishResponse{}, nil
}

func outcomeOf(h *heldJob, request *runnerv1.FinishRequest) (Outcome, error) {
	if request.GetError() != "" {
		return Outcome{Error: &runnerv1.JobError{Code: cmp.Or(request.GetErrorCode(), CodeAgent), Message: request.GetError()}}, nil
	}
	summary, err := h.summary()
	if err != nil {
		return Outcome{}, err
	}
	var exit error
	if code := request.GetExitCode(); code != 0 {
		exit = fmt.Errorf("exit status %d", code)
	}
	outcome := ResumeStream(summary).Outcome(exit)
	if outcome.Error != nil && outcome.Error.Code == CodeRun && len(h.record.StderrTail) > 0 {
		outcome.Error.Message += ": " + strings.Join(h.record.StderrTail, " · ")
	}
	if outcome.Succeeded {
		for _, build := range request.GetBuilds() {
			if naming.ValidateBuildID(build.GetBuildId()) == nil && build.GetApp() != "" {
				outcome.Result.Builds = append(outcome.Result.Builds, build)
			}
		}
	}
	return outcome, nil
}
