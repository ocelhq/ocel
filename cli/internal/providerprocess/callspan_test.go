package providerprocess

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"go.opentelemetry.io/otel/trace"

	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/run"
	"github.com/ocelhq/ocel/cli/internal/runtrace"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	"github.com/ocelhq/ocel/pkg/tracing/tracingtest"
)

func TestACallToTheProviderIsAClientSpanWhoseTraceparentTheProviderReceives(t *testing.T) {
	ctx, recorded := tracingtest.Record(t)
	parent := trace.SpanContextFromContext(ctx)
	var sent string
	call := callSpanInterceptor{}.WrapUnary(func(_ context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		sent = req.Header().Get("traceparent")
		return nil, nil
	})
	if _, err := call(ctx, connect.NewRequest(&contractv1.ConfigureRequest{})); err != nil {
		t.Fatal(err)
	}

	rpc := recorded.Only(t)
	if rpc.SpanKind() != trace.SpanKindClient || rpc.Parent().SpanID() != parent.SpanID() {
		t.Errorf("call is a %s span under %s, want a client span under %s", rpc.SpanKind(), rpc.Parent().SpanID(), parent.SpanID())
	}
	want := "00-" + rpc.SpanContext().TraceID().String() + "-" + rpc.SpanContext().SpanID().String() + "-01"
	if sent != want {
		t.Errorf("traceparent = %q, want %q: the provider's spans nest under the call", sent, want)
	}
}

func TestOneProviderRootStepStreamedByTwoCallsIsTwoSpansEachUnderItsOwnCall(t *testing.T) {
	dir := t.TempDir()
	ctx, traced, err := run.NewBus(time.Now).Begin(context.Background(), "ocel deploy", dir)
	if err != nil {
		t.Fatal(err)
	}
	span := traced.Phase(progressv1.Phase_PHASE_DEPLOY)
	ctx = run.ContextWithSpan(ctx, span)
	p := startFake(t, ctx, "root-step", span, Questions{})
	for range 2 {
		if _, err := Stream(ctx, p, "Deploy", &contractv1.DeployRequest{Manifest: &contractv1.Manifest{Slug: "acme"}}, contractv1connect.ProviderServiceClient.Deploy); err != nil {
			t.Fatal(err)
		}
	}
	var ended error
	traced.End(&ended)

	calls, steps := map[string]bool{}, map[string]string{}
	for _, s := range tracedSpanList(t, tracePathOf(t, dir, traced)) {
		switch s.Name {
		case strings.TrimPrefix(contractv1connect.ProviderServiceDeployProcedure, "/"):
			calls[s.SpanID] = true
		case "Provisioning the environment":
			steps[s.SpanID] = s.ParentSpanID
		}
	}
	if len(calls) != 2 || len(steps) != 2 {
		t.Fatalf("trace holds %d calls and %d distinct steps, want two of each", len(calls), len(steps))
	}
	parents := map[string]bool{}
	for step, parent := range steps {
		if !calls[parent] {
			t.Errorf("step %s nests under %q, want one of the calls %v", step, parent, calls)
		}
		parents[parent] = true
	}
	if len(parents) != 2 {
		t.Errorf("both steps nest under one call, want each under the call that streamed it")
	}
}

func TestAStepALocalProviderExportsItselfIsRecordedOnceInTheRunsTrace(t *testing.T) {
	dir := t.TempDir()
	ctx, traced, err := run.NewBus(time.Now).Begin(context.Background(), "ocel deploy", dir)
	if err != nil {
		t.Fatal(err)
	}
	span := traced.Phase(progressv1.Phase_PHASE_DEPLOY)
	ctx = run.ContextWithSpan(ctx, span)
	p := startFake(t, ctx, "recorded-step", span, Questions{})
	if _, err := Stream(ctx, p, "Deploy", &contractv1.DeployRequest{Manifest: &contractv1.Manifest{Slug: "acme"}}, contractv1connect.ProviderServiceClient.Deploy); err != nil {
		t.Fatal(err)
	}
	p.Close()
	var ended error
	traced.End(&ended)

	if n := countSpansNamed(t, tracePathOf(t, dir, traced), fakeRecordedStepName); n != 1 {
		t.Errorf("trace holds %d %q spans, want the provider's own span alone", n, fakeRecordedStepName)
	}
}

type recordedStepRemote struct {
	contractv1connect.UnimplementedProviderServiceHandler
}

func (recordedStepRemote) Configure(context.Context, *contractv1.ConfigureRequest) (*contractv1.ConfigureResponse, error) {
	return &contractv1.ConfigureResponse{}, nil
}

func (recordedStepRemote) Deploy(_ context.Context, _ *contractv1.DeployRequest, stream *connect.ServerStream[progressv1.OperationEvent]) error {
	if err := sendRecordedStep(stream); err != nil {
		return err
	}
	return stream.Send(&progressv1.OperationEvent{Body: &progressv1.OperationEvent_Result{Result: &progressv1.OperationResult{Success: true}}})
}

type anyToken struct{}

func (anyToken) Ensure(context.Context) (string, error) { return "token", nil }

func TestAStepARemoteProviderSaysItRecordsIsStillInTheRunsTraceSinceItExportsNothingThere(t *testing.T) {
	mux := http.NewServeMux()
	mux.Handle(contractv1connect.NewProviderServiceHandler(recordedStepRemote{}))
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	dir := t.TempDir()
	ctx, traced, err := run.NewBus(time.Now).Begin(context.Background(), "ocel deploy", dir)
	if err != nil {
		t.Fatal(err)
	}
	span := traced.Phase(progressv1.Phase_PHASE_DEPLOY)
	ctx = run.ContextWithSpan(ctx, span)
	p, err := StartRemote(ctx, project.NewUnconfigured(dir, "acme"), span, Questions{}, Remote{Name: "ocel", URL: server.URL, Tokens: anyToken{}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Stream(ctx, p, "Deploy", &contractv1.DeployRequest{Manifest: &contractv1.Manifest{Slug: "acme"}}, contractv1connect.ProviderServiceClient.Deploy); err != nil {
		t.Fatal(err)
	}
	p.Close()
	var ended error
	traced.End(&ended)

	if n := countSpansNamed(t, tracePathOf(t, dir, traced), fakeRecordedStepName); n != 1 {
		t.Errorf("trace holds %d %q spans, want the step its progress events describe", n, fakeRecordedStepName)
	}
}

func TestACallToALocalProviderIsOneClientSpanWithNoSecondBeneathIt(t *testing.T) {
	dir := t.TempDir()
	ctx, traced, err := run.NewBus(time.Now).Begin(context.Background(), "ocel deploy", dir)
	if err != nil {
		t.Fatal(err)
	}
	span := traced.Phase(progressv1.Phase_PHASE_DEPLOY)
	ctx = run.ContextWithSpan(ctx, span)
	p := startFake(t, ctx, "success", span, Questions{})
	if _, err := Stream(ctx, p, "Deploy", &contractv1.DeployRequest{Manifest: &contractv1.Manifest{Slug: "acme"}}, contractv1connect.ProviderServiceClient.Deploy); err != nil {
		t.Fatal(err)
	}
	p.Close()
	var ended error
	traced.End(&ended)

	refuseClientSpansUnderClientSpans(t, tracePathOf(t, dir, traced))
}

func TestACallToARemoteProviderIsOneClientSpanWithNoSecondBeneathIt(t *testing.T) {
	mux := http.NewServeMux()
	mux.Handle(contractv1connect.NewProviderServiceHandler(recordedStepRemote{}))
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	dir := t.TempDir()
	ctx, traced, err := run.NewBus(time.Now).Begin(context.Background(), "ocel deploy", dir)
	if err != nil {
		t.Fatal(err)
	}
	span := traced.Phase(progressv1.Phase_PHASE_DEPLOY)
	ctx = run.ContextWithSpan(ctx, span)
	p, err := StartRemote(ctx, project.NewUnconfigured(dir, "acme"), span, Questions{}, Remote{Name: "ocel", URL: server.URL, Tokens: anyToken{}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Stream(ctx, p, "Deploy", &contractv1.DeployRequest{Manifest: &contractv1.Manifest{Slug: "acme"}}, contractv1connect.ProviderServiceClient.Deploy); err != nil {
		t.Fatal(err)
	}
	p.Close()
	var ended error
	traced.End(&ended)

	refuseClientSpansUnderClientSpans(t, tracePathOf(t, dir, traced))
}

func refuseClientSpansUnderClientSpans(t *testing.T, path string) {
	t.Helper()
	spans := tracedSpanList(t, path)
	clients := map[string]string{}
	for _, s := range spans {
		if s.Kind == "SPAN_KIND_CLIENT" {
			clients[s.SpanID] = s.Name
		}
	}
	if len(clients) == 0 {
		t.Fatal("trace holds no client span for the calls")
	}
	for _, s := range spans {
		if parent, nested := clients[s.ParentSpanID]; nested && s.Kind == "SPAN_KIND_CLIENT" {
			t.Errorf("client span %q nests under client span %q, want one span per call", s.Name, parent)
		}
	}
}

func countSpansNamed(t *testing.T, path, name string) int {
	t.Helper()
	n := 0
	for _, s := range tracedSpanList(t, path) {
		if s.Name == name {
			n++
		}
	}
	return n
}

type tracedSpan struct {
	SpanID       string `json:"spanId"`
	ParentSpanID string `json:"parentSpanId"`
}

func tracedSpans(t *testing.T, path string) map[string]tracedSpan {
	t.Helper()
	byName := map[string]tracedSpan{}
	for _, s := range tracedSpanList(t, path) {
		byName[s.Name] = s.tracedSpan
	}
	return byName
}

type namedSpan struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
	tracedSpan
}

func tracedSpanList(t *testing.T, path string) []namedSpan {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		ResourceSpans []struct {
			ScopeSpans []struct {
				Spans []namedSpan `json:"spans"`
			} `json:"scopeSpans"`
		} `json:"resourceSpans"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	var spans []namedSpan
	for _, resource := range doc.ResourceSpans {
		for _, scope := range resource.ScopeSpans {
			spans = append(spans, scope.Spans...)
		}
	}
	return spans
}

func tracePathOf(t *testing.T, projectDir string, traced *run.Run) string {
	t.Helper()
	found, err := filepath.Glob(filepath.Join(runtrace.RunsDir(projectDir), "*-"+traced.TraceID()[:8], runtrace.TraceFileName))
	if err != nil || len(found) != 1 {
		t.Fatalf("trace files of run %s = %v (err %v), want one", traced.TraceID(), found, err)
	}
	return found[0]
}
