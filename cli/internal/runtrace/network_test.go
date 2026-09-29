package runtrace

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"go.opentelemetry.io/otel/codes"

	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
)

func TestNoNetworkExporterWithoutAnExplicitEndpoint(t *testing.T) {
	for _, v := range otlpEndpointEnvVars {
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

	ctx, r, err := Start(context.Background(), t.TempDir(), "ocel deploy")
	if err != nil {
		t.Fatalf("Start() = %v", err)
	}
	_, span := r.StartSpan(ctx, "build")
	span.SetStatus(codes.Error, "status-describes-sk_live_status")
	span.RecordError(errors.New("event-describes-sk_live_event"))
	span.End()
	id := []byte{9, 9, 9, 9, 9, 9, 9, 9}
	now := time.Now()
	r.Receive(started(id, nil, "orders-db-password-in-a-message"))
	r.Receive(ended(id, now, now.Add(time.Second), progressv1.SpanStatus_SPAN_STATUS_OK))
	if err := r.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}

	mu.Lock()
	body := sent.String()
	mu.Unlock()
	if !strings.Contains(body, "build") || !strings.Contains(body, "provision phase") {
		t.Errorf("sent = %q, want spans named for their span name and phase", body)
	}
	for _, text := range []string{"sk_live_status", "sk_live_event", "orders-db-password-in-a-message"} {
		if strings.Contains(body, text) {
			t.Errorf("sent carries %q, free-form text that must stay on this machine", text)
		}
	}

	local, err := os.ReadFile(strings.TrimSuffix(r.LogPath(), ".ndjson") + ".otlp.json")
	if err != nil {
		t.Fatalf("read trace: %v", err)
	}
	if !strings.Contains(string(local), "orders-db-password-in-a-message") {
		t.Errorf("trace file lost the scope's own name, want the local trace to keep it")
	}
}
