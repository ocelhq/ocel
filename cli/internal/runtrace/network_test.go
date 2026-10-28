package runtrace

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	collectortrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/protobuf/proto"

	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
)

func TestASpanAChildExportsReachesTheNetworkCollectorWithOnlyTheAllowedAttributes(t *testing.T) {
	var mu sync.Mutex
	var sent bytes.Buffer
	exported := map[string][]string{}
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		req := &collectortrace.ExportTraceServiceRequest{}
		_ = proto.Unmarshal(body, req)
		mu.Lock()
		defer mu.Unlock()
		sent.Write(body)
		for _, resource := range req.GetResourceSpans() {
			for _, scope := range resource.GetScopeSpans() {
				for _, s := range scope.GetSpans() {
					var keys []string
					for _, keyValue := range s.GetAttributes() {
						keys = append(keys, keyValue.GetKey())
					}
					exported[string(s.GetSpanId())] = keys
				}
			}
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(collector.Close)
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "")
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", collector.URL)

	r, err := Open(t.TempDir(), "ocel deploy")
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}
	child := exportChildSpan(t, r, "GET orders-db-password-in-a-name",
		attribute.String("http.request.header.authorization", "Bearer sk_live_child"),
		attribute.String("process.command", "pulumi up"))
	if err := r.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	keys, reached := exported[string(child[:])]
	if !reached {
		t.Fatal("the child's span never reached the network collector")
	}
	for _, key := range keys {
		if !isAllowed(attribute.Key(key)) {
			t.Errorf("the child's span carried %q, an attribute outside the allowlist", key)
		}
	}
	for _, text := range []string{"sk_live_child", "orders-db-password-in-a-name"} {
		if strings.Contains(sent.String(), text) {
			t.Errorf("sent carries %q, free-form text that must stay on this machine", text)
		}
	}
}

func TestNoNetworkExporterWithoutAnExplicitEndpoint(t *testing.T) {
	for _, v := range otlpEndpointEnvVariables {
		t.Setenv(v, "")
	}

	exp, err := newNetworkExporter(context.Background())
	if err != nil {
		t.Fatalf("newNetworkExporter() = %v", err)
	}
	if exp != nil {
		t.Error("newNetworkExporter() returned an exporter with no OTLP endpoint configured")
	}
}

func TestNetworkExporterOnlyWhenEndpointConfigured(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "")
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://127.0.0.1:4318")

	exp, err := newNetworkExporter(context.Background())
	if err != nil {
		t.Fatalf("newNetworkExporter() = %v", err)
	}
	if exp == nil {
		t.Fatal("newNetworkExporter() returned nil with OTEL_EXPORTER_OTLP_ENDPOINT set")
	}
	if err := exp.Shutdown(context.Background()); err != nil {
		t.Errorf("Shutdown() = %v", err)
	}
}

func TestWhatTheNetworkExporterSendsCarriesNoFreeFormText(t *testing.T) {
	var mu sync.Mutex
	var sent bytes.Buffer
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		sent.Write(body)
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(collector.Close)
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "")
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", collector.URL)

	r, err := Open(t.TempDir(), "ocel deploy")
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}
	build := []byte{8, 8, 8, 8, 8, 8, 8, 8}
	id := []byte{9, 9, 9, 9, 9, 9, 9, 9}
	now := time.Now()
	r.Receive(Call{}, started(build, nil, "build"))
	failed := ended(build, now, now.Add(time.Second), progressv1.SpanStatus_SPAN_STATUS_ERROR,
		&progressv1.SpanAttribute{Key: progressv1.AttributeKey_ATTRIBUTE_KEY_SPAN_NAME, Value: "build"})
	failed.Operation.Message = "status-describes-sk_live_status"
	r.Receive(Call{}, failed)
	r.Receive(Call{}, started(id, nil, "orders-db-password-in-a-message"))
	r.Receive(Call{}, ended(id, now, now.Add(time.Second), progressv1.SpanStatus_SPAN_STATUS_OK))
	if err := r.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}

	mu.Lock()
	body := sent.String()
	mu.Unlock()
	if !strings.Contains(body, "build") || !strings.Contains(body, "provision phase") {
		t.Errorf("sent = %q, want spans named for their span name and phase", body)
	}
	for _, text := range []string{"sk_live_status", "orders-db-password-in-a-message"} {
		if strings.Contains(body, text) {
			t.Errorf("sent carries %q, free-form text that must stay on this machine", text)
		}
	}

	local, err := os.ReadFile(filepath.Join(r.dir, TraceFileName))
	if err != nil {
		t.Fatalf("read trace: %v", err)
	}
	if !strings.Contains(string(local), "orders-db-password-in-a-message") {
		t.Errorf("trace file lost the scope's own name, want the local trace to keep it")
	}
}
