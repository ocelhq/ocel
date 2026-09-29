package runtrace

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"

	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/ocelhq/ocel/pkg/constants"
	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
)

type ctxKey struct{}

func FromContext(ctx context.Context) *Run {
	r, _ := ctx.Value(ctxKey{}).(*Run)
	return r
}

type Run struct {
	traceID trace.TraceID
	command string
	dir     string

	logPath string
	logMu   sync.Mutex
	logFile *os.File
	send    func(*streamv1.RunEvent)

	scopesMu sync.Mutex
	scopes   map[trace.SpanID]startedScope

	tp       *sdktrace.TracerProvider
	tracer   trace.Tracer
	rootSpan trace.Span

	closeOnce sync.Once
	closeErr  error
}

func Start(ctx context.Context, projectDir, command string) (context.Context, *Run, error) {
	start := time.Now()
	id := newTraceID()
	dir := filepath.Join(projectDir, constants.ProjectStateDirName, "runs")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return ctx, nil, err
	}

	logPath := filepath.Join(dir, id.String()+".ndjson")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
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
		opts = append(opts, sdktrace.WithSpanProcessor(sdktrace.NewBatchSpanProcessor(networkExporter{inner: netExp})))
	}
	tp := sdktrace.NewTracerProvider(opts...)

	r := &Run{
		traceID: id,
		command: command,
		dir:     dir,
		logPath: logPath,
		logFile: logFile,
		scopes:  map[trace.SpanID]startedScope{},
		tp:      tp,
		tracer:  tp.Tracer("github.com/ocelhq/ocel/cli"),
	}
	r.send = r.record

	ctx, root := r.tracer.Start(ctx, command, trace.WithAttributes(AttrCommand.String(command)))
	r.rootSpan = root
	ctx = context.WithValue(ctx, ctxKey{}, r)

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

func (r *Run) StartSpan(ctx context.Context, name string, attrs ...attribute.KeyValue) (context.Context, trace.Span) {
	opts := []trace.SpanStartOption{trace.WithAttributes(attribute.KeyValue{Key: AttrSpanName, Value: attribute.StringValue(name)})}
	if len(attrs) > 0 {
		opts = append(opts, trace.WithAttributes(attrs...))
	}
	return r.tracer.Start(ctx, name, opts...)
}

func (r *Run) LogThrough(send func(*streamv1.RunEvent)) {
	r.send = send
}

func (r *Run) Log(ctx context.Context, app, message string) {
	ev := &streamv1.RunEvent{
		Time:    timestamppb.Now(),
		Level:   progressv1.Level_LEVEL_DEBUG,
		Phase:   progressv1.Phase_PHASE_BUILD,
		Subject: app,
		Message: message,
	}
	if sc := trace.SpanContextFromContext(ctx); sc.HasSpanID() {
		id := sc.SpanID()
		ev.SpanId = id[:]
	}
	r.send(ev)
}

func (r *Run) Close() error {
	r.closeOnce.Do(func() { r.closeErr = r.shutdown() })
	return r.closeErr
}

func (r *Run) shutdown() error {
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
