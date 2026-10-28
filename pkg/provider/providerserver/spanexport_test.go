package providerserver

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/trace"
	collectortrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/protobuf/proto"

	"github.com/ocelhq/ocel/pkg/tracing"
)

func recordingCollector(t *testing.T) (*httptest.Server, *atomic.Value, *atomic.Value) {
	t.Helper()
	var services, authorization atomic.Value
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		req := &collectortrace.ExportTraceServiceRequest{}
		if proto.Unmarshal(raw, req) != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		authorization.Store(r.Header.Get("Authorization"))
		for _, resource := range req.GetResourceSpans() {
			for _, keyValue := range resource.GetResource().GetAttributes() {
				if keyValue.GetKey() == "service.name" {
					services.Store(keyValue.GetValue().GetStringValue())
				}
			}
		}
	}))
	t.Cleanup(server.Close)
	return server, &services, &authorization
}

func keepGlobalTracerProvider(t *testing.T) {
	previous := otel.GetTracerProvider()
	t.Cleanup(func() {
		otel.SetTracerProvider(previous)
		tracing.SetCollector(tracing.Collector{})
	})
}

func TestAProcessHandedACollectorExportsItsSpansThereAsItsServiceWithItsToken(t *testing.T) {
	server, services, authorization := recordingCollector(t)
	t.Setenv(tracing.CollectorURLEnvVar, server.URL+"/v1/traces")
	t.Setenv(tracing.CollectorTokenEnvVar, "run-token")
	keepGlobalTracerProvider(t)

	_, shutdown, err := ExportSpans(context.Background(), "ocel-provider-test")
	if err != nil {
		t.Fatal(err)
	}
	_, span := otel.Tracer("test").Start(context.Background(), "work")
	span.End()
	if err := shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}

	if got, _ := services.Load().(string); got != "ocel-provider-test" {
		t.Errorf("collector saw service %q, want ocel-provider-test", got)
	}
	if got, _ := authorization.Load().(string); got != "Bearer run-token" {
		t.Errorf("collector saw Authorization %q, want the run's token", got)
	}
	if got := tracing.ReadCollector(); got.URL != server.URL+"/v1/traces" || got.Token != "run-token" {
		t.Errorf("ReadCollector() = %+v, want the collector this process was handed, for its own children", got)
	}
}

func TestAProcessHandedNoCollectorExportsNothingEvenWithTheUsersOTLPEndpointSet(t *testing.T) {
	server, services, _ := recordingCollector(t)
	t.Setenv(tracing.CollectorURLEnvVar, "")
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", server.URL)
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", server.URL+"/v1/traces")
	keepGlobalTracerProvider(t)

	_, shutdown, err := ExportSpans(context.Background(), "ocel-provider-test")
	if err != nil {
		t.Fatal(err)
	}
	_, span := otel.Tracer("test").Start(context.Background(), "work")
	span.End()
	if err := shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}

	if got, _ := services.Load().(string); got != "" {
		t.Errorf("the user's collector saw service %q, want nothing exported", got)
	}
}

func TestAProcessHandedACollectorLeavesNoneOfItInTheEnvironmentItsChildrenInherit(t *testing.T) {
	server, _, _ := recordingCollector(t)
	t.Setenv(tracing.CollectorURLEnvVar, server.URL+"/v1/traces")
	t.Setenv(tracing.CollectorTokenEnvVar, "run-token")
	t.Setenv(tracing.TraceParentEnvVar, "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	keepGlobalTracerProvider(t)

	_, shutdown, err := ExportSpans(context.Background(), "ocel-provider-test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = shutdown(context.Background()) })

	for _, name := range []string{tracing.CollectorURLEnvVar, tracing.CollectorTokenEnvVar, tracing.TraceParentEnvVar} {
		if value, set := os.LookupEnv(name); set {
			t.Errorf("%s = %q after ExportSpans, want it unset", name, value)
		}
	}
}

func TestAProcessHandedAParentContinuesThatTraceFromTheContextExportSpansReturns(t *testing.T) {
	t.Setenv(tracing.CollectorURLEnvVar, "")
	t.Setenv(tracing.TraceParentEnvVar, "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	keepGlobalTracerProvider(t)

	ctx, shutdown, err := ExportSpans(context.Background(), "ocel-provider-test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = shutdown(context.Background()) })

	parent := trace.SpanContextFromContext(ctx)
	if parent.TraceID().String() != "4bf92f3577b34da6a3ce929d0e0e4736" || parent.SpanID().String() != "00f067aa0ba902b7" {
		t.Errorf("parent = %s/%s, want the one the process was handed", parent.TraceID(), parent.SpanID())
	}
}
