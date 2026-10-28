package runtrace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"

	"go.opentelemetry.io/otel/exporters/otlp/otlptrace"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"

	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/tracing"
)

type Trace struct {
	traceID trace.TraceID

	dir        string
	eventsMu   sync.Mutex
	eventsFile *os.File
	log        Renderer
	logFile    *runLog

	startedMu sync.Mutex
	started   map[trace.SpanID]startedSpan

	secrets tracing.SecretValues

	children          sync.WaitGroup
	childrenAbandoned chan struct{}
	abandonOnce       sync.Once

	file     sdktrace.SpanProcessor
	network  sdktrace.SpanProcessor
	tracer   trace.Tracer
	rootSpan trace.Span
	unroute  func()

	closeOnce sync.Once
	closeErr  error
}

const (
	cliServiceName       = "ocel"
	instrumentationScope = "github.com/ocelhq/ocel/cli"
)

const networkFlushWindow = 5 * time.Second

var childFlushWindow = 3 * time.Second

func Open(projectDir, command string) (*Trace, error) {
	start := time.Now()
	recording := tracing.ReadMode() != tracing.ModeOff
	tracer, root := startRoot(command, recording)
	id := root.SpanContext().TraceID()
	runs := RunsDir(projectDir)
	dir := filepath.Join(runs, runDirectoryName(start, command, id))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}

	eventsFile, err := os.OpenFile(filepath.Join(dir, EventsFileName), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}

	t := &Trace{
		traceID:           id,
		dir:               dir,
		eventsFile:        eventsFile,
		started:           map[trace.SpanID]startedSpan{},
		childrenAbandoned: make(chan struct{}),
		tracer:            tracer,
		rootSpan:          root,
		unroute:           func() {},
	}
	if recording {
		file, err := otlptrace.New(context.Background(), newTraceFile(filepath.Join(dir, TraceFileName)))
		if err != nil {
			_ = eventsFile.Close()
			return nil, err
		}
		t.file = sdktrace.NewSimpleSpanProcessor(redactingExporter{inner: file, secrets: &t.secrets})
		if exporter, err := newNetworkExporter(context.Background()); err == nil && exporter != nil {
			t.network = sdktrace.NewBatchSpanProcessor(networkExporter{inner: exporter})
		}
		listenForChildSpans()
	}
	openTraces.add(t)

	_ = prune(runs, retainedRuns, start)

	return t, nil
}

func startRoot(command string, recording bool) (trace.Tracer, trace.Span) {
	if !recording {
		unrecorded := trace.NewSpanContext(trace.SpanContextConfig{TraceID: newTraceID(), SpanID: newSpanID()})
		return nil, trace.SpanFromContext(trace.ContextWithSpanContext(context.Background(), unrecorded))
	}
	tracer := tracerProvider().Tracer(instrumentationScope)
	_, root := tracer.Start(context.Background(), command, trace.WithAttributes(Attribute(progress.AttrKeyCommand, command)))
	return tracer, root
}

func (t *Trace) isRecording() bool {
	return t.file != nil
}

func KeepOpen(ctx context.Context) (release func()) {
	t, ok := openTraces.find(trace.SpanContextFromContext(ctx).TraceID())
	if !ok || !t.isRecording() {
		return func() {}
	}
	t.children.Add(1)
	return sync.OnceFunc(t.children.Done)
}

func AddSecretValues(ctx context.Context, values ...string) {
	if t, ok := openTraces.find(trace.SpanContextFromContext(ctx).TraceID()); ok {
		t.secrets.Add(values...)
	}
}

func (t *Trace) StopWaitingForChildren() {
	t.abandonOnce.Do(func() { close(t.childrenAbandoned) })
}

func (t *Trace) waitForChildren(window time.Duration) {
	exited := make(chan struct{})
	go func() {
		t.children.Wait()
		close(exited)
	}()
	deadline := time.NewTimer(window)
	defer deadline.Stop()
	select {
	case <-exited:
	case <-t.childrenAbandoned:
	case <-deadline.C:
	}
}

func (t *Trace) ID() string {
	return t.traceID.String()
}

func (t *Trace) ContextWithRootSpan(ctx context.Context) context.Context {
	return trace.ContextWithSpan(ctx, t.rootSpan)
}

func (t *Trace) ContextWithSpanID(ctx context.Context, raw []byte) context.Context {
	id, ok := spanID(raw)
	if !ok {
		return ctx
	}
	return trace.ContextWithRemoteSpanContext(ctx, trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    t.traceID,
		SpanID:     id,
		TraceFlags: trace.FlagsSampled,
	}))
}

func (t *Trace) Close() error {
	t.closeOnce.Do(func() { t.closeErr = t.shutdown() })
	return t.closeErr
}

func (t *Trace) shutdown() error {
	t.rootSpan.End()
	t.waitForChildren(childFlushWindow)
	openTraces.remove(t)
	var errs []error
	if t.isRecording() {
		errs = append(errs, t.file.Shutdown(context.Background()))
	}
	if t.network != nil {
		ctx, cancel := context.WithTimeout(context.Background(), networkFlushWindow)
		defer cancel()
		select {
		case <-t.childrenAbandoned:
			cancel()
		default:
		}
		errs = append(errs, t.network.Shutdown(ctx))
	}

	errs = append(errs, t.closeLog())
	t.eventsMu.Lock()
	errs = append(errs, t.eventsFile.Close())
	t.eventsMu.Unlock()
	return errors.Join(errs...)
}
