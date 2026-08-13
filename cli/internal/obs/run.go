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

func Start(ctx context.Context, projectDir, command string) (context.Context, *Run, error) {
	start := time.Now()
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
	fileExp := newFileExporter(tracePath)
	opts := []sdktrace.TracerProviderOption{
		sdktrace.WithIDGenerator(fixedIDGenerator{traceID: id}),
		sdktrace.WithSpanProcessor(sdktrace.NewSimpleSpanProcessor(allowlistExporter{inner: fileExp})),
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

	_ = Prune(dir, RunRetention, start)

	return ctx, r, nil
}

func (r *Run) TraceID() string {
	return r.traceID.String()
}

func (r *Run) Command() string {
	return r.command
}

func (r *Run) Dir() string {
	return r.dir
}

func (r *Run) LogPath() string {
	return r.logPath
}

func (r *Run) Tracer() trace.Tracer {
	return r.tracer
}

func (r *Run) StartSpan(ctx context.Context, stage string, attrs ...attribute.KeyValue) (context.Context, trace.Span) {
	opts := []trace.SpanStartOption{trace.WithAttributes(attribute.KeyValue{Key: AttrStage, Value: attribute.StringValue(stage)})}
	if len(attrs) > 0 {
		opts = append(opts, trace.WithAttributes(attrs...))
	}
	return r.tracer.Start(ctx, stage, opts...)
}

func (r *Run) Log(ctx context.Context, level Level, stage Stage, app App, message string, attrs ...attribute.KeyValue) {
	rec := logRecord{
		Time:    time.Now().UTC(),
		Level:   level,
		Message: message,
		Stage:   string(stage),
		App:     string(app),
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

func (r *Run) Debug(ctx context.Context, stage Stage, app App, message string, attrs ...attribute.KeyValue) {
	r.Log(ctx, LevelDebug, stage, app, message, attrs...)
}

func (r *Run) Info(ctx context.Context, stage Stage, app App, message string, attrs ...attribute.KeyValue) {
	r.Log(ctx, LevelInfo, stage, app, message, attrs...)
}

func (r *Run) Warn(ctx context.Context, stage Stage, app App, message string, attrs ...attribute.KeyValue) {
	r.Log(ctx, LevelWarn, stage, app, message, attrs...)
}

func (r *Run) Error(ctx context.Context, stage Stage, app App, message string, attrs ...attribute.KeyValue) {
	r.Log(ctx, LevelError, stage, app, message, attrs...)
}

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
