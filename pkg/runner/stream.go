package runner

import (
	"bytes"
	"cmp"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"google.golang.org/protobuf/encoding/protojson"

	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	runnerv1 "github.com/ocelhq/ocel/pkg/proto/runner/v1"
)

const maxMessage = 8 << 10

const (
	CodeSource      = "job.source"
	CodeRun         = "job.run"
	CodeAgent       = "job.agent"
	CodeTimeout     = "job.timeout"
	CodeInterrupted = "job.interrupted"
)

func Codes() []string {
	return []string{CodeSource, CodeRun, CodeAgent, CodeTimeout, CodeInterrupted}
}

type Progress struct {
	Level   runnerv1.JobEventLevel
	Phase   runnerv1.JobPhase
	Message string
	Step    string
	Input   *runnerv1.JobInput
	Resumed bool
}

type Outcome struct {
	Succeeded bool
	Headline  string
	Error     *runnerv1.JobError
	Result    *runnerv1.JobResult
}

type Stream struct {
	summary *streamv1.RunSummary
	failure *streamv1.RunError
}

type failureDocument struct {
	OK    *bool           `json:"ok"`
	Error json.RawMessage `json:"error"`
}

func ResumeStream(summary *streamv1.RunSummary) *Stream { return &Stream{summary: summary} }

func (s *Stream) Summary() *streamv1.RunSummary { return s.summary }

var lenient = protojson.UnmarshalOptions{DiscardUnknown: true}

func (s *Stream) Line(line []byte) (Progress, bool) {
	line = bytes.TrimSpace(line)
	if len(line) == 0 {
		return Progress{}, false
	}
	if failure, isFailure := readFailure(line); isFailure {
		s.failure = failure
		return Progress{Level: runnerv1.JobEventLevel_JOB_EVENT_LEVEL_ERROR, Message: Clip(describeFailure(failure))}, true
	}
	event := &streamv1.RunEvent{}
	if err := lenient.Unmarshal(line, event); err != nil {
		return Progress{Level: runnerv1.JobEventLevel_JOB_EVENT_LEVEL_DEBUG, Message: Clip(string(line))}, true
	}
	if summary := event.GetSummary(); summary != nil {
		s.summary = summary
		return Progress{}, false
	}
	if input := event.GetWaiting().GetInput(); input != nil {
		return Progress{Level: runnerv1.JobEventLevel_JOB_EVENT_LEVEL_WARN, Message: Clip(describeInput(input)), Step: "Waiting for input", Input: input}, true
	}
	if resumed := event.GetResumed(); resumed != nil {
		return Progress{Level: runnerv1.JobEventLevel_JOB_EVENT_LEVEL_INFO, Message: Clip(cmp.Or(resumed.GetReason(), "Resumed")), Resumed: true}, true
	}
	op := event.GetOperation()
	if op == nil {
		return Progress{}, false
	}
	progress := Progress{Level: levelOf(op.GetLevel()), Phase: phaseOf(op.GetPhase())}
	named := func(text string) string {
		text, _, _ = strings.Cut(text, "\n")
		if op.GetSubject() == "" {
			return text
		}
		if text == "" {
			return op.GetSubject()
		}
		return op.GetSubject() + ": " + text
	}
	switch body := op.GetBody().(type) {
	case *progressv1.OperationEvent_Started:
		if op.GetSubject() == "" && op.GetMessage() == "" {
			return progress, progress.Phase != runnerv1.JobPhase_JOB_PHASE_UNSPECIFIED
		}
		progress.Message = named(op.GetMessage())
		progress.Step = progress.Message
	case *progressv1.OperationEvent_Ended:
		text := cmp.Or(body.Ended.GetTitle(), op.GetMessage())
		if body.Ended.GetStatus() == progressv1.SpanStatus_SPAN_STATUS_ERROR {
			progress.Level = runnerv1.JobEventLevel_JOB_EVENT_LEVEL_ERROR
			progress.Message = named(cmp.Or(text, "failed"))
			if op.GetSubject() != "" && op.GetMessage() != "" && body.Ended.GetTitle() != "" {
				progress.Message += ": " + op.GetMessage()
			}
			break
		}
		if text == "" {
			return Progress{}, false
		}
		progress.Message = named(text)
	case *progressv1.OperationEvent_Output:
		progress.Message = op.GetMessage()
		if op.GetSubject() != "" {
			progress.Message = op.GetSubject() + " | " + progress.Message
		}
	case *progressv1.OperationEvent_Result:
		if body.Result.GetError() != "" {
			progress.Level = runnerv1.JobEventLevel_JOB_EVENT_LEVEL_ERROR
			progress.Message = named(body.Result.GetError())
			break
		}
		if op.GetMessage() == "" {
			return Progress{}, false
		}
		progress.Message = named(op.GetMessage())
	default:
		if op.GetMessage() == "" {
			return Progress{}, false
		}
		progress.Message = named(op.GetMessage())
	}
	progress.Message = Clip(progress.Message)
	progress.Step = Clip(progress.Step)
	return progress, progress.Message != ""
}

func (s *Stream) Outcome(exit error) Outcome {
	summary := s.summary
	if summary == nil && s.failure != nil {
		return Outcome{Error: &runnerv1.JobError{Code: cmp.Or(s.failure.GetCode(), CodeRun), Message: describeFailure(s.failure)}}
	}
	if summary == nil {
		message := "the ocel run ended without a summary"
		if exit != nil {
			message += " (" + exit.Error() + ")"
		}
		return Outcome{Error: &runnerv1.JobError{Code: CodeRun, Message: message}}
	}
	outcome := Outcome{Succeeded: summary.GetSuccess(), Headline: summary.GetHeadline(), Result: resultOf(summary)}
	if !outcome.Succeeded {
		outcome.Error = &runnerv1.JobError{
			Code:    cmp.Or(summary.GetError().GetCode(), CodeRun),
			Message: cmp.Or(summary.GetError().GetMessage(), summary.GetDetail(), summary.GetHeadline(), "the ocel run failed"),
		}
	}
	return outcome
}

func readFailure(line []byte) (*streamv1.RunError, bool) {
	var document failureDocument
	if err := json.Unmarshal(line, &document); err != nil || document.OK == nil || *document.OK || len(document.Error) == 0 {
		return nil, false
	}
	failure := &streamv1.RunError{}
	if err := lenient.Unmarshal(document.Error, failure); err != nil {
		return nil, false
	}
	return failure, true
}

func describeFailure(failure *streamv1.RunError) string {
	message := cmp.Or(failure.GetMessage(), "the ocel run failed")
	if hint := failure.GetHint(); hint != "" {
		return message + " (hint: " + hint + ")"
	}
	return message
}

func AnswerLine(answer *streamv1.InputAnswer) ([]byte, error) {
	line, err := protojson.Marshal(answer)
	if err != nil {
		return nil, err
	}
	return append(line, '\n'), nil
}

func describeInput(input *runnerv1.JobInput) string {
	if needed := input.GetVariables(); needed != nil {
		keys := make([]string, 0, len(needed.GetVariables()))
		for _, variable := range needed.GetVariables() {
			keys = append(keys, strings.TrimPrefix(variable.GetFolder()+"/", "/")+variable.GetKey())
		}
		return "Waiting for values for " + strings.Join(keys, ", ")
	}
	confirmation := input.GetConfirmation()
	if finding := confirmation.GetFinding(); finding != "" {
		return "Waiting for an answer: " + strings.ReplaceAll(finding, "\n", " ") + " " + confirmation.GetQuestion()
	}
	return "Waiting for an answer: " + confirmation.GetQuestion()
}

func resultOf(summary *streamv1.RunSummary) *runnerv1.JobResult {
	result := &runnerv1.JobResult{}
	for _, app := range summary.GetApps() {
		for _, url := range app.GetUrls() {
			if !slices.Contains(result.Urls, url) {
				result.Urls = append(result.Urls, url)
			}
		}
	}
	return result
}

func levelOf(level progressv1.Level) runnerv1.JobEventLevel {
	if _, ok := runnerv1.JobEventLevel_name[int32(level)]; ok {
		return runnerv1.JobEventLevel(level)
	}
	return runnerv1.JobEventLevel_JOB_EVENT_LEVEL_INFO
}

func phaseOf(phase progressv1.Phase) runnerv1.JobPhase {
	if _, ok := runnerv1.JobPhase_name[int32(phase)]; ok {
		return runnerv1.JobPhase(phase)
	}
	return runnerv1.JobPhase_JOB_PHASE_UNSPECIFIED
}

func Clip(text string) string {
	if len(text) <= maxMessage {
		return text
	}
	cut := maxMessage
	for cut > 0 && text[cut]&0xC0 == 0x80 {
		cut--
	}
	return text[:cut] + fmt.Sprintf("… (%d more bytes)", len(text)-cut)
}
