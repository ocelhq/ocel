package runner_test

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"buf.build/go/protovalidate"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	runnerv1 "github.com/ocelhq/ocel/pkg/proto/runner/v1"
	"github.com/ocelhq/ocel/pkg/runner"
)

func readStream(t *testing.T, name string) (*runner.Stream, []runner.Progress) {
	t.Helper()
	file, err := os.Open(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	stream := &runner.Stream{}
	var told []runner.Progress
	scanner := bufio.NewScanner(file)
	scanner.Buffer(nil, 1<<20)
	for scanner.Scan() {
		if progress, ok := stream.Line(scanner.Bytes()); ok {
			told = append(told, progress)
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return stream, told
}

func messages(told []runner.Progress) []string {
	var said []string
	for _, progress := range told {
		if progress.Message != "" {
			said = append(said, progress.Message)
		}
	}
	return said
}

func TestABuildsStreamTellsItsStepsAndLinesAndEndsSucceeded(t *testing.T) {
	stream, told := readStream(t, "build.ndjson")
	var steps []string
	for _, progress := range told {
		if progress.Step != "" {
			steps = append(steps, progress.Step)
		}
	}
	if want := []string{"ocel: Loading the provider", "hello: Building app hello"}; !slices.Equal(steps, want) {
		t.Fatalf("steps %q, want %q", steps, want)
	}
	said := messages(told)
	for _, want := range []string{
		"ocel: Loaded the provider",
		"hello | [2/3] WORKDIR /app",
		"hello: Built app hello",
		"Built the image of hello as ocel/hello/hello@sha256:9af9c9ed1f085a8081f5debe7090b764be769481ec40cd3aa74e2aec3b89ede9",
	} {
		if !slices.Contains(said, want) {
			t.Errorf("no line %q among %q", want, said)
		}
	}
	if told[0].Phase != runnerv1.JobPhase_JOB_PHASE_CHECK || told[0].Message != "" {
		t.Fatalf("the stream starts with %+v, want the check phase, saying nothing", told[0])
	}
	if last := told[len(told)-1]; last.Phase != runnerv1.JobPhase_JOB_PHASE_BUILD || last.Level != runnerv1.JobEventLevel_JOB_EVENT_LEVEL_INFO {
		t.Fatalf("the stream ends with %+v, want an info line of the build phase", last)
	}

	outcome := stream.Outcome(nil)
	if !outcome.Succeeded || outcome.Error != nil || outcome.Headline != "Built 1 image" {
		t.Fatalf("outcome %+v, want succeeded with the run's headline", outcome)
	}
}

func TestADeployRefusedByTheProviderEndsFailedWithTheSummarysError(t *testing.T) {
	stream, told := readStream(t, "deploy.ndjson")
	last := told[len(told)-1]
	if last.Level != runnerv1.JobEventLevel_JOB_EVENT_LEVEL_ERROR || !strings.Contains(last.Message, "bootstrap the target first") {
		t.Fatalf("the failed span tells %+v, want an error line with the refusal", last)
	}
	outcome := stream.Outcome(errors.New("exit status 1"))
	if outcome.Succeeded || outcome.Headline != "Deploy failed" || outcome.Error.GetCode() != "internal" ||
		!strings.Contains(outcome.Error.GetMessage(), "bootstrap the target first") {
		t.Fatalf("outcome %+v, want failed with the summary's code and message", outcome)
	}
}

func TestAStreamWithNoSummaryFailsAndALineThatIsNoEventIsKept(t *testing.T) {
	stream := &runner.Stream{}
	progress, ok := stream.Line([]byte("panic: something broke\n"))
	if !ok || progress.Level != runnerv1.JobEventLevel_JOB_EVENT_LEVEL_DEBUG || progress.Message != "panic: something broke" {
		t.Fatalf("a line that is no event told %+v, %v, want it kept as a debug line", progress, ok)
	}
	if _, ok := stream.Line([]byte("  \n")); ok {
		t.Fatal("a blank line told something")
	}
	outcome := stream.Outcome(errors.New("exit status 2"))
	if outcome.Succeeded || outcome.Error.GetCode() != runner.CodeRun || !strings.Contains(outcome.Error.GetMessage(), "without a summary") {
		t.Fatalf("outcome %+v, want failed for want of a summary", outcome)
	}
}

func TestARunThatFailsBeforeItsFirstEventFailsTheJobWithItsOwnError(t *testing.T) {
	stream := &runner.Stream{}
	line := `{"ok":false,"error":{"code":"console.not_linked","message":"this directory is not linked to a console project","hint":"ocel link","retryable":false}}`
	progress, ok := stream.Line([]byte(line + "\n"))
	if !ok || progress.Level != runnerv1.JobEventLevel_JOB_EVENT_LEVEL_ERROR || !strings.Contains(progress.Message, "not linked to a console project") {
		t.Fatalf("the failure document told %+v, %v, want its message as an error", progress, ok)
	}
	outcome := stream.Outcome(errors.New("exit status 1"))
	if outcome.Succeeded || outcome.Error.GetCode() != "console.not_linked" ||
		!strings.Contains(outcome.Error.GetMessage(), "not linked to a console project") || !strings.Contains(outcome.Error.GetMessage(), "ocel link") {
		t.Fatalf("outcome %+v, want the run's own code, message and hint", outcome)
	}
}

func TestARunWaitingOnInputTellsWhatItNeedsWithoutValuesThenResumes(t *testing.T) {
	stream := &runner.Stream{}
	waiting, ok := stream.Line([]byte(`{"waiting":{"input":{"id":"in_0123456789abcdef","variables":{"tier":"TIER_PRODUCTION",` +
		`"variables":[{"key":"GREETING","description":"Said first"},{"key":"TOKEN","folder":"/api","secret":true}]}}}}`))
	if !ok || waiting.Input.GetId() != "in_0123456789abcdef" || waiting.Step != "Waiting for input" {
		t.Fatalf("a waiting event told %+v, %v, want its input and a step", waiting, ok)
	}
	if waiting.Message != "Waiting for values for GREETING, api/TOKEN" {
		t.Errorf("message %q, want the keys it waits on", waiting.Message)
	}

	asked, _ := stream.Line([]byte(`{"waiting":{"input":{"id":"in_1","confirmation":{"finding":"No existing deployment for slug \"x\".\nThis will create a NEW project.","question":"Continue?"}}}}`))
	if asked.Input.GetConfirmation().GetQuestion() != "Continue?" || asked.Message != `Waiting for an answer: No existing deployment for slug "x". This will create a NEW project. Continue?` {
		t.Errorf("a confirmation told %+v, want the finding and the question on one line", asked)
	}

	resumed, ok := stream.Line([]byte(`{"resumed":{"reason":"answered in the console"}}`))
	if !ok || !resumed.Resumed || resumed.Input != nil || resumed.Message != "answered in the console" {
		t.Fatalf("a resumed event told %+v, %v, want it resumed with its reason", resumed, ok)
	}

	if plain, ok := stream.Line([]byte(`{"waiting":{"url":"http://127.0.0.1:1/variables"}}`)); ok {
		t.Errorf("a waiting event with no input told %+v, want nothing: only an input pauses a job", plain)
	}
}

func TestAnAnswerIsOneInputAnswerLineAsTheRunReadsIt(t *testing.T) {
	for _, tt := range []struct {
		answer *streamv1.InputAnswer
		want   string
	}{
		{&streamv1.InputAnswer{InputId: "in_0123456789abcdef", Answer: &streamv1.InputAnswer_Resolved{Resolved: true}}, `{"inputId":"in_0123456789abcdef","resolved":true}`},
		{&streamv1.InputAnswer{InputId: "in_1", Answer: &streamv1.InputAnswer_Confirmed{Confirmed: false}}, `{"inputId":"in_1","confirmed":false}`},
	} {
		line, err := runner.AnswerLine(tt.answer)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasSuffix(string(line), "\n") || strings.Count(string(line), "\n") != 1 {
			t.Fatalf("line %q, want exactly one, ending in a newline", line)
		}
		var got, want map[string]any
		if err := json.Unmarshal(line, &got); err != nil {
			t.Fatal(err)
		}
		_ = json.Unmarshal([]byte(tt.want), &want)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("line %s, want %s", line, tt.want)
		}
		read := &streamv1.InputAnswer{}
		if err := protojson.Unmarshal(line, read); err != nil || protovalidate.Validate(read) != nil || !proto.Equal(read, tt.answer) {
			t.Errorf("the run reads %s as %v (%v), want %v", line, read, err, tt.answer)
		}
	}
}

func TestASucceededRunsResultNamesEachURLItsAppsServeOnce(t *testing.T) {
	line, err := protojson.Marshal(&streamv1.RunEvent{Cli: &streamv1.RunEvent_Summary{Summary: &streamv1.RunSummary{
		Success: true, Headline: "Deployed hello",
		Apps: []*progressv1.AppResult{{Urls: []string{"https://hello.example.com"}}, {Urls: []string{"https://hello.example.com", "https://api.example.com"}}},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	stream := &runner.Stream{}
	if told, ok := stream.Line(line); ok {
		t.Fatalf("the summary told %+v, want nothing in the job's log", told)
	}
	outcome := stream.Outcome(nil)
	if want := []string{"https://hello.example.com", "https://api.example.com"}; !outcome.Succeeded || !slices.Equal(outcome.Result.GetUrls(), want) {
		t.Fatalf("outcome %+v, want succeeded with the URLs %q", outcome, want)
	}
}
