package run

import (
	"context"
	"errors"
	"slices"
	"sync"
	"time"

	"github.com/ocelhq/ocel/cli/internal/runtrace"
	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	"google.golang.org/protobuf/proto"
)

type Sink interface {
	Receive(ev *streamv1.RunEvent)
	Close() error
}

type Bus struct {
	now func() time.Time

	mu    sync.Mutex
	sinks []Sink
	runs  []*Run
}

func NewBus(now func() time.Time) *Bus {
	return &Bus{now: now}
}

func (b *Bus) Attach(s Sink) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.sinks = append(b.sinks, s)
}

func (b *Bus) Begin(ctx context.Context, command, projectDir string) (context.Context, *Run, error) {
	r := &Run{bus: b, command: command, start: b.now(), phases: map[progressv1.Phase]*Span{}}
	if projectDir != "" {
		var err error
		if r.trace, err = runtrace.Open(projectDir, command); err != nil {
			return ctx, nil, err
		}
		b.Attach(r.trace)
	}
	r.ctx = ctx
	b.mu.Lock()
	b.runs = append(b.runs, r)
	b.mu.Unlock()
	return ctx, r, nil
}

func (b *Bus) Interrupt() {
	b.mu.Lock()
	runs := slices.Clone(b.runs)
	b.mu.Unlock()
	for _, r := range runs {
		r.interrupt()
	}
	_ = b.Close()
}

func (b *Bus) finish(r *Run) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.runs = slices.DeleteFunc(b.runs, func(open *Run) bool { return open == r })
}

func (b *Bus) detach(s Sink) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.sinks = slices.DeleteFunc(b.sinks, func(attached Sink) bool { return attached == s })
}

func (b *Bus) send(ev *streamv1.RunEvent) *streamv1.RunEvent {
	b.stamp(ev)
	shown := proto.CloneOf(ev)
	collapse(shown.ProtoReflect())
	orderPlan(shown.GetPlan())
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, s := range b.sinks {
		s.Receive(shown)
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
	return errors.Join(errs...)
}
