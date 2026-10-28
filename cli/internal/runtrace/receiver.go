package runtrace

import (
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"sync"
	"time"

	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	collectortrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonv1 "go.opentelemetry.io/proto/otlp/common/v1"
	tracev1 "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"

	"github.com/ocelhq/ocel/pkg/localrpc"
	"github.com/ocelhq/ocel/pkg/tracing"
)

const (
	maxExportBytes = 16 << 20
	serviceNameKey = "service.name"
)

var receiving sync.Once

func listenForChildSpans() {
	receiving.Do(func() {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return
		}
		address, token := listener.Addr().String(), localrpc.NewSessionToken()
		mux := http.NewServeMux()
		mux.HandleFunc("POST "+tracing.CollectorPath, receiveSpans)
		go func() { _ = http.Serve(listener, localrpc.LoopbackGuard(address, token, mux)) }()
		tracing.SetCollector(tracing.Collector{URL: "http://" + address + tracing.CollectorPath, Token: token})
	})
}

func receiveSpans(w http.ResponseWriter, r *http.Request) {
	if mediaType, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); mediaType != tracing.CollectorMediaType {
		http.Error(w, "the collector takes OTLP/HTTP in "+tracing.CollectorMediaType+" only", http.StatusUnsupportedMediaType)
		return
	}
	body := io.Reader(http.MaxBytesReader(w, r.Body, maxExportBytes))
	if r.Header.Get("Content-Encoding") == "gzip" {
		unzipped, err := gzip.NewReader(body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		defer unzipped.Close()
		body = unzipped
	}
	raw, err := io.ReadAll(io.LimitReader(body, maxExportBytes+1))
	var tooLarge *http.MaxBytesError
	switch {
	case errors.As(err, &tooLarge) || len(raw) > maxExportBytes:
		http.Error(w, fmt.Sprintf("an export is at most %d bytes", maxExportBytes), http.StatusRequestEntityTooLarge)
		return
	case err != nil:
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	req := &collectortrace.ExportTraceServiceRequest{}
	if err := proto.Unmarshal(raw, req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	for _, resourceSpans := range req.GetResourceSpans() {
		service := serviceName(resourceSpans.GetResource().GetAttributes())
		for _, scopeSpans := range resourceSpans.GetScopeSpans() {
			for _, s := range scopeSpans.GetSpans() {
				replayChildSpan(s, service, scopeSpans.GetScope().GetName())
			}
		}
	}
	resp, _ := proto.Marshal(&collectortrace.ExportTraceServiceResponse{})
	w.Header().Set("Content-Type", tracing.CollectorMediaType)
	_, _ = w.Write(resp)
}

var childProviders sync.Map

func childTracer(service, scope string) trace.Tracer {
	if provider, found := childProviders.Load(service); found {
		return provider.(*sdktrace.TracerProvider).Tracer(scope)
	}
	provider, _ := childProviders.LoadOrStore(service, newTracerProvider(service))
	return provider.(*sdktrace.TracerProvider).Tracer(scope)
}

func replayChildSpan(s *tracev1.Span, service, scope string) {
	traceID, ok := traceIDOf(s.GetTraceId())
	if !ok {
		return
	}
	id, ok := spanID(s.GetSpanId())
	if !ok {
		return
	}
	parentID, ok := spanID(s.GetParentSpanId())
	if !ok {
		return
	}
	if _, open := openTraces.find(traceID); !open {
		return
	}
	ctx := trace.ContextWithRemoteSpanContext(context.Background(), trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: traceID, SpanID: parentID, TraceFlags: trace.FlagsSampled, Remote: true,
	}))
	_, span := childTracer(service, scope).Start(contextWithSpanID(ctx, id), s.GetName(),
		trace.WithTimestamp(time.Unix(0, int64(s.GetStartTimeUnixNano()))),
		trace.WithSpanKind(trace.SpanKind(s.GetKind())),
		trace.WithAttributes(attributesOf(s.GetAttributes())...),
	)
	for _, event := range s.GetEvents() {
		span.AddEvent(event.GetName(), trace.WithTimestamp(time.Unix(0, int64(event.GetTimeUnixNano()))), trace.WithAttributes(attributesOf(event.GetAttributes())...))
	}
	switch s.GetStatus().GetCode() {
	case tracev1.Status_STATUS_CODE_OK:
		span.SetStatus(codes.Ok, "")
	case tracev1.Status_STATUS_CODE_ERROR:
		span.SetStatus(codes.Error, s.GetStatus().GetMessage())
	}
	span.End(trace.WithTimestamp(time.Unix(0, int64(s.GetEndTimeUnixNano()))))
}

func traceIDOf(raw []byte) (trace.TraceID, bool) {
	var id trace.TraceID
	if len(raw) != len(id) {
		return id, false
	}
	copy(id[:], raw)
	return id, id.IsValid()
}

func serviceName(resource []*commonv1.KeyValue) string {
	for _, keyValue := range resource {
		if keyValue.GetKey() == serviceNameKey {
			return keyValue.GetValue().GetStringValue()
		}
	}
	return ""
}
