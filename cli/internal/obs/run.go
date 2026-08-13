// Package obs is the seam every ocel command instruments against: one Run
// per invocation, carrying a run identity that is its OpenTelemetry trace
// ID, a structured log sink, and a tracer — all writing artifacts under the
// project's .ocel directory that a human, or an OTLP viewer, can read after
// the command exits.
package obs

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

// Run is a single command's run. Every real ocel command creates exactly one
// via Start, uses it to log events and open spans for the duration of the
// command, then Closes it. The Run's trace ID is the run's identity: it
// names both files this package writes and is the only ID a human, CI, or
// support ticket ever needs to quote.
type Run struct {
	traceID trace.TraceID
	command string
	dir     string

	logPath string
	logMu   sync.Mutex
	logFile *os.File

	tp       *sdktrace.TracerProvider
	tracer   trace.Tracer
	rootSpan trace.Span
}

// Start begins a run for command, rooted at the given context, writing
// artifacts under projectDir/.ocel/runs. It returns a context carrying the
// run's root span, so any child span opened against that context — via
// Run.StartSpan or the standard otel APIs using Run.Tracer() — is attached
// to this run's trace automatically.
func Start(ctx context.Context, projectDir, command string) (context.Context, *Run, error) {
	id := newTraceID()
	dir := filepath.Join(projectDir, ".ocel", "runs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return ctx, nil, err
	}

	logPath := filepath.Join(dir, id.String()+".ndjson")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return ctx, nil, err
	}

	tracePath := filepath.Join(dir, id.String()+".otlp.json")
	opts := []sdktrace.TracerProviderOption{
		sdktrace.WithIDGenerator(fixedIDGenerator{traceID: id}),
		sdktrace.WithSpanProcessor(sdktrace.NewSimpleSpanProcessor(allowlistExporter{inner: newFileExporter(tracePath)})),
	}
	if netExp, netErr := newNetworkExporter(ctx); netErr == nil && netExp != nil {
		opts = append(opts, sdktrace.WithSpanProcessor(sdktrace.NewBatchSpanProcessor(allowlistExporter{inner: netExp})))
	}
	tp := sdktrace.NewTracerProvider(opts...)

	r := &Run{
		traceID: id,
		command: command,
		dir:     dir,
		logPath: logPath,
		logFile: logFile,
		tp:      tp,
		tracer:  tp.Tracer("github.com/ocelhq/ocel/cli"),
	}

	ctx, root := r.tracer.Start(ctx, command, trace.WithAttributes(AttrCommand.String(command)))
	r.rootSpan = root

	_ = Prune(dir, RunRetention)

	return ctx, r, nil
}

// TraceID is the run's identity: the OTel trace ID shared by every span in
// this run and stamped on every log record, formatted as lowercase hex.
func (r *Run) TraceID() string {
	return r.traceID.String()
}

// LogPath is the NDJSON log file this run writes to.
func (r *Run) LogPath() string {
	return r.logPath
}

// Tracer returns the run's tracer. Spans started from it — directly, or via
// StartSpan — are part of this run's trace and are subject to the attribute
// allowlist on export; nothing about creating or annotating a span needs to
// know that.
func (r *Run) Tracer() trace.Tracer {
	return r.tracer
}

// StartSpan starts a child span under ctx's current span (the run's root
// span if ctx is the one Start returned and nothing has replaced it). attrs
// not in the allowlist are silently dropped when the span is exported, not
// when it is set — SetAttributes on the returned Span is exempt from
// nothing, so callers do not need to pre-filter.
func (r *Run) StartSpan(ctx context.Context, stage string, attrs ...attribute.KeyValue) (context.Context, trace.Span) {
	opts := []trace.SpanStartOption{trace.WithAttributes(attribute.KeyValue{Key: AttrStage, Value: attribute.StringValue(stage)})}
	if len(attrs) > 0 {
		opts = append(opts, trace.WithAttributes(attrs...))
	}
	return r.tracer.Start(ctx, stage, opts...)
}

// Log writes one structured log record. The record carries the run's trace
// ID always, and the active span's ID when ctx has one — StartSpan's
// returned context, or one derived from it. attrs outside the allowlist are
// dropped before the record is written.
func (r *Run) Log(ctx context.Context, level Level, stage, app, message string, attrs ...attribute.KeyValue) {
	rec := logRecord{
		Time:    time.Now().UTC(),
		Level:   level,
		Message: message,
		Stage:   stage,
		App:     app,
		TraceID: r.TraceID(),
	}
	if sc := trace.SpanContextFromContext(ctx); sc.HasSpanID() {
		rec.SpanID = sc.SpanID().String()
	}
	if filtered := filterAttributes(attrs); len(filtered) > 0 {
		rec.Attrs = make(map[string]any, len(filtered))
		for _, a := range filtered {
			rec.Attrs[string(a.Key)] = a.Value.AsInterface()
		}
	}

	raw, err := json.Marshal(rec)
	if err != nil {
		return
	}
	raw = append(raw, '\n')

	r.logMu.Lock()
	defer r.logMu.Unlock()
	_, _ = r.logFile.Write(raw)
}

func (r *Run) Debug(ctx context.Context, stage, app, message string, attrs ...attribute.KeyValue) {
	r.Log(ctx, LevelDebug, stage, app, message, attrs...)
}

func (r *Run) Info(ctx context.Context, stage, app, message string, attrs ...attribute.KeyValue) {
	r.Log(ctx, LevelInfo, stage, app, message, attrs...)
}

func (r *Run) Warn(ctx context.Context, stage, app, message string, attrs ...attribute.KeyValue) {
	r.Log(ctx, LevelWarn, stage, app, message, attrs...)
}

func (r *Run) Error(ctx context.Context, stage, app, message string, attrs ...attribute.KeyValue) {
	r.Log(ctx, LevelError, stage, app, message, attrs...)
}

// Close ends the run's root span, flushes and shuts down the tracer
// provider — writing the final OTLP/JSON trace file — and closes the log
// file. It is safe to call exactly once, after which the Run is done.
func (r *Run) Close() error {
	r.rootSpan.End()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	shutdownErr := r.tp.Shutdown(ctx)

	r.logMu.Lock()
	closeErr := r.logFile.Close()
	r.logMu.Unlock()

	if shutdownErr != nil {
		return shutdownErr
	}
	return closeErr
}
