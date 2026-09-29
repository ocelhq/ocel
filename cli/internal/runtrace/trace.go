package runtrace

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"time"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"

	"github.com/ocelhq/ocel/pkg/constants"
	"github.com/ocelhq/ocel/pkg/progress"
)

type Trace struct {
	traceID trace.TraceID

	logPath string
	logMu   sync.Mutex
	logFile *os.File

	startedMu sync.Mutex
	started   map[trace.SpanID]startedSpan

	tp       *sdktrace.TracerProvider
	tracer   trace.Tracer
	rootSpan trace.Span

	closeOnce sync.Once
	closeErr  error
}

func Open(projectDir, command string) (*Trace, error) {
	start := time.Now()
	id := newTraceID()
	dir := filepath.Join(projectDir, constants.ProjectStateDirName, "runs")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}

	logPath := filepath.Join(dir, id.String()+".ndjson")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}

	tracePath := filepath.Join(dir, id.String()+".otlp.json")
	opts := []sdktrace.TracerProviderOption{
		sdktrace.WithIDGenerator(fixedIDGenerator{traceID: id}),
		sdktrace.WithSpanProcessor(sdktrace.NewSimpleSpanProcessor(allowlistExporter{inner: newFileExporter(tracePath)})),
	}
	if netExp, netErr := newNetworkExporter(context.Background()); netErr == nil && netExp != nil {
		opts = append(opts, sdktrace.WithSpanProcessor(sdktrace.NewBatchSpanProcessor(networkExporter{inner: netExp})))
	}
	tp := sdktrace.NewTracerProvider(opts...)

	t := &Trace{
		traceID: id,
		logPath: logPath,
		logFile: logFile,
		started: map[trace.SpanID]startedSpan{},
		tp:      tp,
		tracer:  tp.Tracer("github.com/ocelhq/ocel/cli"),
	}
	_, t.rootSpan = t.tracer.Start(context.Background(), command, trace.WithAttributes(Attribute(progress.AttrKeyCommand, command)))

	_ = prune(dir, retainedRuns, start)

	return t, nil
}

func (t *Trace) LogPath() string {
	return t.logPath
}

func (t *Trace) Close() error {
	t.closeOnce.Do(func() { t.closeErr = t.shutdown() })
	return t.closeErr
}

func (t *Trace) shutdown() error {
	t.rootSpan.End()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	shutdownErr := t.tp.Shutdown(ctx)

	t.logMu.Lock()
	closeErr := t.logFile.Close()
	t.logMu.Unlock()

	if shutdownErr != nil {
		return shutdownErr
	}
	return closeErr
}
