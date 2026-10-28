package run

import (
	"context"
	"errors"
	"io"
	"slices"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/ocelhq/ocel/cli/internal/runtrace"
	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
)

type Sink interface {
	Receive(ev *streamv1.RunEvent)
	Close() error
}

type Bus struct {
	now       func() time.Time
	renderLog func(w io.Writer) Sink

	mu     sync.Mutex
	sinks  []Sink
	traces []*runtrace.Trace
	runs   []*Run
}

func NewBus(now func() time.Time) *Bus {
	return &Bus{now: now}
}

func (b *Bus) Attach(s Sink) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.sinks = append(b.sinks, s)
}

func (b *Bus) LogRunsWith(render func(w io.Writer) Sink) {
	b.renderLog = render
}

func (b *Bus) Begin(ctx context.Context, command, projectDir string) (context.Context, *Run, error) {
	r := &Run{bus: b, command: command, start: b.now(), phases: map[progressv1.Phase]*Span{}}
	if projectDir != "" {
		var err error
		if r.trace, err = runtrace.Open(projectDir, command); err != nil {
			return ctx, nil, err
		}
		if b.renderLog != nil {
			if err := r.trace.RenderLog(func(w io.Writer) runtrace.Renderer { return b.renderLog(w) }); err != nil {
				_ = r.trace.Close()
				return ctx, nil, err
			}
		}
		b.mu.Lock()
		b.traces = append(b.traces, r.trace)
		b.mu.Unlock()
		ctx = r.trace.ContextWithRootSpan(ctx)
	}
	r.ctx = ctx
	b.mu.Lock()
	b.runs = append(b.runs, r)
	b.mu.Unlock()
	return ctx, r, nil
}

func (b *Bus) Preamble(ctx context.Context) *Span {
	r := &Run{bus: b, ctx: ctx, start: b.now(), phases: map[progressv1.Phase]*Span{}}
	return &Span{run: r, start: r.start, level: progressv1.Level_LEVEL_INFO}
}

func (b *Bus) Warn(message string) {
	b.send(&streamv1.RunEvent{Operation: &progressv1.OperationEvent{Level: progressv1.Level_LEVEL_WARN, Message: message}}, receiveOutsideCall)
}

func (b *Bus) Interrupt() {
	b.mu.Lock()
	runs := slices.Clone(b.runs)
	b.mu.Unlock()
	for _, r := range runs {
		if r.trace != nil {
			r.trace.StopWaitingForChildren()
		}
		r.interrupt()
	}
	_ = b.Close()
}

func (b *Bus) finish(r *Run) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.runs = slices.DeleteFunc(b.runs, func(open *Run) bool { return open == r })
}

func (b *Bus) detachTrace(t *runtrace.Trace) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.traces = slices.DeleteFunc(b.traces, func(attached *runtrace.Trace) bool { return attached == t })
}

func receiveOutsideCall(t *runtrace.Trace, event *streamv1.RunEvent) {
	t.Receive(runtrace.Call{}, event)
}

func (b *Bus) send(event *streamv1.RunEvent, toTrace func(*runtrace.Trace, *streamv1.RunEvent)) *streamv1.RunEvent {
	b.stamp(event)
	shown := proto.CloneOf(event)
	collapse(shown.ProtoReflect())
	orderPlan(shown.GetOperation().GetPlan())
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, s := range b.sinks {
		s.Receive(shown)
	}
	for _, t := range b.traces {
		toTrace(t, shown)
	}
	return shown
}

func (b *Bus) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	var errs []error
	for _, s := range b.sinks {
		errs = append(errs, s.Close())
	}
	for _, t := range b.traces {
		errs = append(errs, t.Close())
	}
	return errors.Join(errs...)
}
