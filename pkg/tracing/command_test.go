package tracing_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"

	"github.com/ocelhq/ocel/pkg/tracing"
	"github.com/ocelhq/ocel/pkg/tracing/content"
	"github.com/ocelhq/ocel/pkg/tracing/tracingtest"
)

func TestACommandIsASpanWithItsArgumentsExitCodeAndTheTailOfItsOutput(t *testing.T) {
	ctx, recorded := tracingtest.Record(t)
	cmd := tracing.Command(ctx, "sh", "-c", "echo progress; echo denied >&2; exit 3")
	cmd.Dir = t.TempDir()
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr

	if err := cmd.Run(); err == nil {
		t.Fatal("Run() = nil, want the exit status")
	}
	if stdout.String() != "progress\n" || stderr.String() != "denied\n" {
		t.Errorf("caller's writers got %q / %q, want the whole output", stdout.String(), stderr.String())
	}

	span := recorded.Only(t)
	attrs := tracingtest.Attributes(span)
	if span.Name() != "sh" {
		t.Errorf("span name = %q, want the executable", span.Name())
	}
	for key, want := range map[string]string{
		"process.command_args":      `["sh","-c","echo progress; echo denied >&2; exit 3"]`,
		"process.working_directory": cmd.Dir,
		"process.exit.code":         "3",
		"process.stdout.tail":       "progress\n",
		"process.stderr.tail":       "denied\n",
	} {
		if got := attrs[key].String(); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
	if attrs["process.pid"].AsInt64() == 0 {
		t.Error("process.pid unset")
	}
	if span.Status().Code != codes.Error {
		t.Errorf("status = %v, want Error", span.Status().Code)
	}
}

func TestACommandsOutputReachesTheCallerAndItsTailTheSpan(t *testing.T) {
	ctx, recorded := tracingtest.Record(t)
	out, err := tracing.Command(ctx, "echo", "hello").Output()
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != "hello\n" {
		t.Errorf("Output() = %q", out)
	}
	if got := tracingtest.Attributes(recorded.Only(t))["process.stdout.tail"].AsString(); got != "hello\n" {
		t.Errorf("process.stdout.tail = %q", got)
	}
}

func TestAFailedOutputCallKeepsItsStderrForTheCallerAndTheSpan(t *testing.T) {
	ctx, recorded := tracingtest.Record(t)
	_, err := tracing.Command(ctx, "sh", "-c", "echo no such stack >&2; exit 1").Output()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || string(exit.Stderr) != "no such stack\n" {
		t.Fatalf("Output() err = %v, want an ExitError carrying stderr", err)
	}
	if got := tracingtest.Attributes(recorded.Only(t))["process.stderr.tail"].AsString(); got != "no such stack\n" {
		t.Errorf("process.stderr.tail = %q", got)
	}
}

func TestACommandWhoseOutputGoesToAFileLeavesTheFileInPlace(t *testing.T) {
	ctx, _ := tracingtest.Record(t)
	out, err := os.CreateTemp(t.TempDir(), "out")
	if err != nil {
		t.Fatal(err)
	}
	cmd := tracing.Command(ctx, "echo", "to file")
	cmd.Stdout = out
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	if cmd.Stdout != out {
		t.Error("Stdout replaced, want the caller's file untouched")
	}
}

func runCollectorAt(t *testing.T, url, token string) {
	t.Helper()
	t.Setenv(tracing.CollectorURLEnvVar, "")
	t.Setenv(tracing.CollectorTokenEnvVar, "")
	t.Cleanup(func() { tracing.SetCollector(tracing.Collector{}) })
	tracing.SetCollector(tracing.Collector{URL: url, Token: token})
}

const printTraceEnv = `printf '%s|%s|%s' "$TRACEPARENT" "$OCEL_TRACE_COLLECTOR_URL" "$OCEL_TRACE_COLLECTOR_TOKEN"`

func TestACommandExportingSpansIsHandedItsOwnSpanAsParentAndTheRunsCollector(t *testing.T) {
	ctx, recorded := tracingtest.Record(t)
	runCollectorAt(t, "http://127.0.0.1:4318/v1/traces", "run-token")
	t.Setenv("TRACEPARENT", "")
	cmd := tracing.Command(ctx, "sh", "-c", printTraceEnv)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "NODE_ENV=production"}
	cmd.ExportsSpans = true

	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}

	span := recorded.Only(t)
	want := "00-" + span.SpanContext().TraceID().String() + "-" + span.SpanContext().SpanID().String() + "-01|http://127.0.0.1:4318/v1/traces|run-token"
	if string(out) != want {
		t.Errorf("child saw %q, want %q: its spans nest under its own process span and export to the run's collector", out, want)
	}
	attrs := tracingtest.Attributes(span)
	if attrs["process.env.NODE_ENV"].AsString() != "production" {
		t.Errorf("process.env.NODE_ENV = %q, want the caller's variable recorded", attrs["process.env.NODE_ENV"].AsString())
	}
	for _, name := range []string{"TRACEPARENT", "OCEL_TRACE_COLLECTOR_URL", "OCEL_TRACE_COLLECTOR_TOKEN"} {
		if value, recordedVariable := attrs["process.env."+name]; recordedVariable {
			t.Errorf("process.env.%s = %q, want what tracing hands the child left off its span", name, value.AsString())
		}
	}
}

func TestACommandNotExportingSpansIsHandedNeitherAParentNorTheCollector(t *testing.T) {
	ctx, _ := tracingtest.Record(t)
	runCollectorAt(t, "http://127.0.0.1:4318/v1/traces", "run-token")
	t.Setenv("TRACEPARENT", "")

	out, err := tracing.Command(ctx, "sh", "-c", printTraceEnv).Output()
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != "||" {
		t.Errorf("child saw %q, want no trace parent and no collector token", out)
	}
}

func TestACommandIsExportingSpansOnlyOnceStartedWithTheRunsCollectorAndATraceToNestUnder(t *testing.T) {
	traced, _ := tracingtest.Record(t)
	for _, tc := range []struct {
		name         string
		ctx          context.Context
		collector    string
		exportsSpans bool
		want         bool
	}{
		{"asked, with a collector, in a trace", traced, "http://127.0.0.1:4318/v1/traces", true, true},
		{"asked, with no collector", traced, "", true, false},
		{"asked, outside any trace", context.Background(), "http://127.0.0.1:4318/v1/traces", true, false},
		{"not asked", traced, "http://127.0.0.1:4318/v1/traces", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runCollectorAt(t, tc.collector, "run-token")
			cmd := tracing.Command(tc.ctx, "true")
			cmd.ExportsSpans = tc.exportsSpans
			if cmd.IsExportingSpans() {
				t.Error("IsExportingSpans() = true before the command started")
			}
			if err := cmd.Run(); err != nil {
				t.Fatal(err)
			}
			if got := cmd.IsExportingSpans(); got != tc.want {
				t.Errorf("IsExportingSpans() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestACommandLeavesTheEnvironmentItsChildInheritsAsTheCallerSetIt(t *testing.T) {
	ctx, _ := tracingtest.Record(t)
	t.Setenv("TRACEPARENT", "")
	inherited := tracing.Command(ctx, "sh", "-c", "printf %s \"$TRACEPARENT\"")
	out, err := inherited.Output()
	if err != nil {
		t.Fatal(err)
	}
	explicit := tracing.Command(ctx, "true")
	explicit.Env = []string{"PATH=" + os.Getenv("PATH")}
	if err := explicit.Run(); err != nil {
		t.Fatal(err)
	}

	if string(out) != "" || inherited.Env != nil {
		t.Errorf("child saw TRACEPARENT %q and Env %q, want the environment left as the caller set it", out, inherited.Env)
	}
	if !slices.Equal(explicit.Env, []string{"PATH=" + os.Getenv("PATH")}) {
		t.Errorf("Env = %q, want the caller's", explicit.Env)
	}
}

func TestAnExplicitVariableIsRecordedAsSetAndTheTracesRedactionHidesItUnlessItIsKnownNotToBeSecret(t *testing.T) {
	ctx, recorded := tracingtest.Record(t)
	t.Setenv("OCEL_INHERITED", "x")
	cmd := tracing.Command(ctx, "true")
	explicit := map[string]string{
		"NODE_ENV":       "production",
		"PORT":           "3000",
		"DATABASE_URL":   "postgres://ocel:hunter2@db/app",
		"STRIPE_WEBHOOK": "whsec_live",
	}
	cmd.Env = os.Environ()
	for name, value := range explicit {
		cmd.Env = append(cmd.Env, name+"="+value)
	}
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}

	span := recorded.Only(t)
	attrs := tracingtest.Attributes(span)
	for name, value := range explicit {
		if got := attrs["process.env."+name].AsString(); got != value {
			t.Errorf("process.env.%s = %q, want %q as the caller set it", name, got, value)
		}
	}
	if _, ok := attrs["process.env.OCEL_INHERITED"]; ok {
		t.Error("recorded an inherited variable")
	}
	exported := map[string]string{}
	for _, keyValue := range new(tracing.SecretValues).RedactAttributes(span.Attributes()) {
		exported[string(keyValue.Key)] = keyValue.Value.AsString()
	}
	for name, want := range map[string]string{
		"NODE_ENV":       "production",
		"PORT":           "3000",
		"DATABASE_URL":   tracing.Redacted,
		"STRIPE_WEBHOOK": tracing.Redacted,
	} {
		if got := exported["process.env."+name]; got != want {
			t.Errorf("exported process.env.%s = %q, want %q", name, got, want)
		}
	}
}

func TestACommandOutlivesTheEndOfTheContextItsSpanStartsUnder(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cmd := tracing.Command(ctx, "sleep", "0.2")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	cancel()
	if err := cmd.Wait(); err != nil {
		t.Errorf("Wait() = %v, want the command left to finish after its context ended", err)
	}
}

func TestACommandMadeWithItsContextIsStoppedWhenTheContextEnds(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cmd := tracing.CommandContext(ctx, "sleep", "5")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	cancel()
	if err := cmd.Wait(); err == nil {
		t.Error("Wait() = nil, want the kill its context's end caused")
	}
	if waited := time.Since(started); waited > 2*time.Second {
		t.Errorf("waited %s, want the command stopped when its context ended", waited)
	}
}

func TestTheTailOfLongOutputIsItsLastBytesStartingOnACharacter(t *testing.T) {
	ctx, recorded := tracingtest.Record(t)
	script := "i=0; while [ $i -lt 3000 ]; do printf 'é line %04d\\n' $i; i=$((i+1)); done"
	cmd := tracing.Command(ctx, "sh", "-c", script)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}

	whole := stdout.String()
	got := tracingtest.Attributes(recorded.Only(t))["process.stdout.tail"].AsString()
	if !strings.HasSuffix(whole, got) || len(got) > tracing.TailBytes || len(got) < tracing.TailBytes-utf8.UTFMax {
		t.Errorf("process.stdout.tail holds %d bytes, want the last %d of the output", len(got), tracing.TailBytes)
	}
	if !utf8.ValidString(got) {
		t.Error("process.stdout.tail starts inside a character")
	}
}

func TestACommandStartedAndWaitedOnIsOneSpan(t *testing.T) {
	ctx, recorded := tracingtest.Record(t)
	cmd := tracing.Command(ctx, "true")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	if got := tracingtest.Attributes(recorded.Only(t))["process.exit.code"].AsInt64(); got != 0 {
		t.Errorf("process.exit.code = %d", got)
	}
}

func TestACommandStartedFromAContextOmittingContentIsASpanWithoutItsOutputAndTheCallerStillReadsIt(t *testing.T) {
	ctx, recorded := tracingtest.Record(t)
	omitted := content.Omit(ctx)
	run := tracing.Command(omitted, "sh", "-c", "echo secret-a; echo secret-b >&2; exit 2")
	var stdout bytes.Buffer
	run.Stdout = &stdout
	_ = run.Run()
	out, _ := tracing.Command(omitted, "sh", "-c", "echo secret-c; echo secret-d >&2; exit 2").Output()

	if stdout.String() != "secret-a\n" || string(out) != "secret-c\n" {
		t.Errorf("caller read %q and %q, want the whole output", stdout.String(), out)
	}
	ended := recorded.Ended()
	if len(ended) != 2 {
		t.Fatalf("ended %d spans, want 2", len(ended))
	}
	for _, span := range ended {
		attrs := tracingtest.Attributes(span)
		for _, key := range []string{"process.stdout.tail", "process.stderr.tail"} {
			if _, recordedOutput := attrs[key]; recordedOutput {
				t.Errorf("%s = %q, want no output on the span", key, attrs[key].AsString())
			}
		}
		if attrs["process.exit.code"].AsInt64() != 2 || len(attrs["process.command_args"].AsStringSlice()) != 3 {
			t.Errorf("span attributes = %v, want the argv and exit code kept", attrs)
		}
	}
}

func TestACommandThatCannotStartIsAnErrorSpan(t *testing.T) {
	ctx, recorded := tracingtest.Record(t)
	if err := tracing.Command(ctx, "/nonexistent/ocel-test-binary").Run(); err == nil {
		t.Fatal("Run() = nil")
	}
	if got := recorded.Only(t).Status().Code; got != codes.Error {
		t.Errorf("status = %v, want Error", got)
	}
}

func TestACommandReadThroughItsPipesKeepsTheTailOfWhatTheyCarriedOnItsSpan(t *testing.T) {
	ctx, recorded := tracingtest.Record(t)
	cmd := tracing.Command(ctx, "sh", "-c", "echo pulled; echo 'manifest unknown' >&2")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	said, _ := io.ReadAll(stdout)
	complained, _ := io.ReadAll(stderr)
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	if string(said) != "pulled\n" || string(complained) != "manifest unknown\n" {
		t.Errorf("caller read %q / %q, want the whole output", said, complained)
	}

	attrs := tracingtest.Attributes(recorded.Only(t))
	for key, want := range map[string]string{"process.stdout.tail": "pulled\n", "process.stderr.tail": "manifest unknown\n"} {
		if got := attrs[key].AsString(); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
}

func TestACommandFedFromAReaderCountsTheBytesItsStdinWasSent(t *testing.T) {
	ctx, recorded := tracingtest.Record(t)
	cmd := tracing.Command(ctx, "sh", "-c", "cat >/dev/null")
	cmd.Stdin = strings.NewReader("twelve bytes")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}

	if got := tracingtest.Attributes(recorded.Only(t))["process.stdin.bytes"].AsInt64(); got != 12 {
		t.Errorf("process.stdin.bytes = %d, want the 12 bytes sent", got)
	}
}

func TestAnAttributeSetBeforeACommandStartsIsOnItsSpan(t *testing.T) {
	ctx, recorded := tracingtest.Record(t)
	cmd := tracing.Command(ctx, "true")
	cmd.SetAttributes(attribute.String("ssh.principal", "ubuntu@203.0.113.10"))
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}

	if got := tracingtest.Attributes(recorded.Only(t))["ssh.principal"].AsString(); got != "ubuntu@203.0.113.10" {
		t.Errorf("ssh.principal = %q, want the attribute set before the command ran", got)
	}
}

func TestACommandGivenOneWriterForBothStreamsWritesItFromOnePipeAndRecordsItsTailOnce(t *testing.T) {
	ctx, recorded := tracingtest.Record(t)
	cmd := tracing.Command(ctx, "sh", "-c", `for i in $(seq 1 2000); do echo out-$i; echo err-$i >&2; done`)
	var said bytes.Buffer
	cmd.Stdout, cmd.Stderr = &said, &said

	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	if lines := strings.Count(said.String(), "\n"); lines != 4000 {
		t.Fatalf("the writer holds %d lines, want 4000", lines)
	}

	attrs := tracingtest.Attributes(recorded.Only(t))
	if tail := attrs["process.stdout.tail"].AsString(); !strings.HasSuffix(tail, "out-2000\nerr-2000\n") {
		t.Errorf("process.stdout.tail ends %q, want both streams in the order they were written", tail[max(0, len(tail)-40):])
	}
	if _, recordedTwice := attrs["process.stderr.tail"]; recordedTwice {
		t.Error("process.stderr.tail set, want the shared output recorded once")
	}
}
