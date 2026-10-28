package pulumi

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/blang/semver"
	sdk "github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/tracing"
	"github.com/ocelhq/ocel/pkg/tracing/tracingtest"
)

type recordedRun struct {
	env    []string
	args   []string
	stderr string
	code   int
	err    error
}

func (r *recordedRun) Run(_ context.Context, _ string, _ io.Reader, _, _ []io.Writer, env []string, args ...string) (string, string, int, error) {
	r.env, r.args = env, args
	return "", r.stderr, r.code, r.err
}

func (*recordedRun) Version() semver.Version { return semver.MustParse(PinnedVersion) }

func TestEachPulumiCommandIsASpanWhoseTraceparentReachesTheCommand(t *testing.T) {
	t.Parallel()
	ctx, recorded := tracingtest.Record(t)
	run := &recordedRun{}
	command := tracedCommand{PulumiCommand: run}

	if _, _, _, err := command.Run(ctx, t.TempDir(), nil, nil, nil, []string{"PULUMI_HOME=/tmp/p"}, "stack", "select", "--stack", "prod", "--create"); err != nil {
		t.Fatal(err)
	}

	got := tracingtest.SpanNamed(t, recorded.Ended(), "pulumi stack select")
	if v := tracingtest.Attributes(got)["process.command_args"]; !slices.Equal(v.AsStringSlice(), []string{"pulumi", "stack", "select", "--stack", "prod", "--create"}) {
		t.Errorf("process.command_args = %q", v.AsStringSlice())
	}
	want := tracing.TraceParentEnvVar + "=00-" + got.SpanContext().TraceID().String() + "-" + got.SpanContext().SpanID().String() + "-01"
	if !slices.Contains(run.env, want) || !slices.Contains(run.env, "PULUMI_HOME=/tmp/p") {
		t.Errorf("command env = %q, want PULUMI_HOME kept and %q", run.env, want)
	}
}

func TestAFailedPulumiCommandIsAnErrorSpanWithItsStderrMasked(t *testing.T) {
	t.Parallel()
	ctx, recorded := tracingtest.Record(t)
	run := &recordedRun{stderr: "error: passphrase hunter2 is wrong", code: 255, err: errors.New("exit status 255")}
	command := tracedCommand{PulumiCommand: run, secrets: []string{"hunter2"}}

	_, _, _, _ = command.Run(ctx, t.TempDir(), nil, nil, nil, nil, "up", "--yes")

	got := tracingtest.SpanNamed(t, recorded.Ended(), "pulumi up")
	if got.Status().Code != codes.Error {
		t.Fatalf("status = %v, want Error", got.Status().Code)
	}
	if v := tracingtest.Attributes(got)["process.exit.code"]; v.AsInt64() != 255 {
		t.Errorf("process.exit.code = %d, want 255", v.AsInt64())
	}
	v := tracingtest.Attributes(got)["process.stderr.tail"]
	if strings.Contains(v.AsString(), "hunter2") || !strings.Contains(v.AsString(), tracing.Redacted) {
		t.Errorf("process.stderr.tail = %q, want the passphrase masked", v.AsString())
	}
}

func TestEachPulumiCommandExportsItsOwnSpansThroughALoopbackEndpointThatHoldsNoCollectorToken(t *testing.T) {
	t.Parallel()
	ctx, recorded := tracingtest.Record(t)
	run := &recordedRun{}
	collector := tracing.Collector{URL: "http://127.0.0.1:4318/v1/traces", Token: "run-token"}
	command := tracedCommand{PulumiCommand: run, collector: collector}

	if _, _, _, err := command.Run(ctx, t.TempDir(), nil, nil, nil, nil, "up", "--yes"); err != nil {
		t.Fatal(err)
	}

	if len(run.args) != 4 || run.args[0] != "--otel-traces" || !slices.Equal(run.args[2:], []string{"up", "--yes"}) {
		t.Fatalf("command args = %q, want --otel-traces <endpoint> ahead of the command", run.args)
	}
	endpoint, err := url.Parse(run.args[1])
	if err != nil {
		t.Fatal(err)
	}
	if endpoint.Hostname() != "127.0.0.1" || endpoint.Host == "127.0.0.1:4318" || strings.Contains(run.args[1], "run-token") {
		t.Errorf("--otel-traces %q, want a loopback endpoint of its own that never carries the collector's token", run.args[1])
	}
	got := tracingtest.SpanNamed(t, recorded.Ended(), "pulumi up")
	if !slices.Contains(run.env, tracing.TraceParentEnvVar+"=00-"+got.SpanContext().TraceID().String()+"-"+got.SpanContext().SpanID().String()+"-01") {
		t.Errorf("command env = %q, want the command span as its TRACEPARENT", run.env)
	}
	args := tracingtest.Attributes(got)["process.command_args"].AsStringSlice()
	relayToken, bearer := strings.CutPrefix(endpoint.Query().Get("Authorization"), "Bearer ")
	if !bearer || relayToken == "" || strings.Contains(strings.Join(args, " "), relayToken) {
		t.Errorf("process.command_args = %q, want the endpoint's own token masked", args)
	}
}

func TestAPulumiCommandWithNoCollectorExportsNoSpans(t *testing.T) {
	t.Parallel()
	ctx, _ := tracingtest.Record(t)
	run := &recordedRun{}

	if _, _, _, err := (tracedCommand{PulumiCommand: run}).Run(ctx, t.TempDir(), nil, nil, nil, nil, "up", "--yes"); err != nil {
		t.Fatal(err)
	}

	if !slices.Equal(run.args, []string{"up", "--yes"}) {
		t.Errorf("command args = %q, want the command alone", run.args)
	}
}

func TestPulumisOwnSpansReachTheCollectorUnderTheCommandThatRanThem(t *testing.T) {
	if testing.Short() {
		t.Skip("this drives the real engine, which installs and runs the pinned Pulumi runtime")
	}
	collector, handed := startGuardedCollector(t, "run-token")
	before := tracing.ReadCollector()
	tracing.SetCollector(handed)
	t.Cleanup(func() { tracing.SetCollector(before) })
	ctx, recorded := tracingtest.Record(t)
	automation := New(Config[struct{}]{
		Backend: Backend{URL: "file://" + t.TempDir(), Passphrase: "a-passphrase", Project: "ocel-trace-probe"},
		Program: func(*sdk.Context, struct{}) error { return nil },
	})
	ref := provider.StackRef{Project: "probe", Tier: environment.TierPreview, Name: naming.InfraStack("pr-1")}

	if _, err := automation.Run(ctx, ref, struct{}{}, provider.Lease{}, nil); err != nil {
		t.Fatalf("Run() = %v", err)
	}

	up := tracingtest.SpanNamed(t, recorded.Ended(), "pulumi up")
	var root *tracepb.Span
	for _, s := range collector.received() {
		if raw, _ := proto.Marshal(s.span); bytes.Contains(raw, []byte("a-passphrase")) {
			t.Errorf("Pulumi's span %q carries the state passphrase", s.span.GetName())
		}
		if s.service == "pulumi-cli" && s.span.GetName() == "pulumi" && bytes.Equal(s.span.GetParentSpanId(), spanIDBytes(up)) {
			root = s.span
		}
	}
	if root == nil {
		t.Fatalf("the collector received %q, want Pulumi's root span under the pulumi up span", collector.names())
	}
	if !bytes.Equal(root.GetTraceId(), traceIDBytes(up)) {
		t.Errorf("Pulumi's root span is in trace %x, want the run's trace %s", root.GetTraceId(), up.SpanContext().TraceID())
	}
}

func spanIDBytes(span sdktrace.ReadOnlySpan) []byte {
	id := span.SpanContext().SpanID()
	return id[:]
}

func traceIDBytes(span sdktrace.ReadOnlySpan) []byte {
	id := span.SpanContext().TraceID()
	return id[:]
}
