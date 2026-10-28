package pulumi

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/common/util/otelreceiver"
	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"

	"github.com/ocelhq/ocel/pkg/localrpc"
	"github.com/ocelhq/ocel/pkg/tracing"
)

type collectedSpan struct {
	service string
	span    *tracepb.Span
}

type guardedCollector struct {
	mu    sync.Mutex
	spans []collectedSpan
}

func (c *guardedCollector) received() []collectedSpan {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.spans)
}

func (c *guardedCollector) names() []string {
	var names []string
	for _, s := range c.received() {
		names = append(names, s.span.GetName())
	}
	return names
}

func serviceOf(resource *resourcepb.Resource) string {
	for _, keyValue := range resource.GetAttributes() {
		if keyValue.GetKey() == "service.name" {
			return keyValue.GetValue().GetStringValue()
		}
	}
	return ""
}

func startGuardedCollector(t *testing.T, token string) (*guardedCollector, tracing.Collector) {
	t.Helper()
	collector := &guardedCollector{}
	server := httptest.NewUnstartedServer(nil)
	server.Config.Handler = localrpc.LoopbackGuard(server.Listener.Addr().String(), token, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		req := &coltracepb.ExportTraceServiceRequest{}
		if err := proto.Unmarshal(raw, req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		collector.mu.Lock()
		defer collector.mu.Unlock()
		for _, resourceSpans := range req.GetResourceSpans() {
			for _, scopeSpans := range resourceSpans.GetScopeSpans() {
				for _, s := range scopeSpans.GetSpans() {
					collector.spans = append(collector.spans, collectedSpan{service: serviceOf(resourceSpans.GetResource()), span: s})
				}
			}
		}
	}))
	server.Start()
	t.Cleanup(server.Close)
	return collector, tracing.Collector{URL: server.URL + tracing.CollectorPath, Token: token}
}

func pulumiSpans(name string) []*tracepb.ResourceSpans {
	return []*tracepb.ResourceSpans{{ScopeSpans: []*tracepb.ScopeSpans{{Spans: []*tracepb.Span{{
		TraceId: []byte("0123456789abcdef"), SpanId: []byte("01234567"), Name: name,
	}}}}}}
}

func TestTheSpanRelayHandsPulumisExportToTheCollectorUnderTheCollectorsToken(t *testing.T) {
	t.Parallel()
	collector, handed := startGuardedCollector(t, "run-token")
	relay, err := startSpanRelay(handed)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = relay.Close() })
	exporter, err := otelreceiver.NewExporter(relay.endpoint)
	if err != nil {
		t.Fatal(err)
	}

	if err := exporter.ExportSpans(context.Background(), pulumiSpans("pulumi-plan")); err != nil {
		t.Fatalf("ExportSpans() through the relay = %v", err)
	}

	if got := collector.names(); len(got) != 1 || got[0] != "pulumi-plan" {
		t.Errorf("the collector received %q, want Pulumi's span", got)
	}
}

func TestTheSpanRelayRefusesAnExportWithoutItsOwnToken(t *testing.T) {
	t.Parallel()
	collector, handed := startGuardedCollector(t, "run-token")
	relay, err := startSpanRelay(handed)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = relay.Close() })
	endpoint, err := url.Parse(relay.endpoint)
	if err != nil {
		t.Fatal(err)
	}
	endpoint.RawQuery = ""
	exporter, err := otelreceiver.NewExporter(endpoint.String())
	if err != nil {
		t.Fatal(err)
	}

	err = exporter.ExportSpans(context.Background(), pulumiSpans("injected"))

	if err == nil || !strings.Contains(err.Error(), "403") {
		t.Errorf("ExportSpans() without the relay's token = %v, want it refused", err)
	}
	if got := collector.names(); len(got) != 0 {
		t.Errorf("the collector received %q from an export that held no token", got)
	}
}
