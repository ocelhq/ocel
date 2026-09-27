package events

import (
	"context"
	"errors"
	"sync"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/ocelhq/ocel/cli/internal/runtrace"
	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
)

type Sink interface {
	Receive(ev *streamv1.RunEvent)
	Close() error
}

type Bus struct {
	now func() time.Time

	mu    sync.Mutex
	sinks []Sink
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
	r := &Run{bus: b, command: command, start: b.now(), phases: map[progressv1.Phase]*Scope{}}
	if projectDir != "" {
		var err error
		if ctx, r.trace, err = runtrace.Start(ctx, projectDir, command); err != nil {
			return ctx, nil, err
		}
	}
	r.ctx = ctx
	return ctx, r, nil
}

func (b *Bus) Send(ev *streamv1.RunEvent) {
	if ev.GetTime() == nil {
		ev.Time = timestamppb.New(b.now())
	}
	if ev.GetLevel() == progressv1.Level_LEVEL_UNSPECIFIED {
		ev.Level = progressv1.Level_LEVEL_INFO
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, s := range b.sinks {
		s.Receive(ev)
	}
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
