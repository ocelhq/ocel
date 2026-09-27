package runui

import (
	"context"
	"io"
	"sync"
	"time"

	"github.com/ocelhq/ocel/cli/internal/events"
	"github.com/ocelhq/ocel/cli/internal/providerclient"
	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
)

type Check func(scope *events.Scope, prov *providerclient.Provider) error

func PlainCheck(present Presentation, w io.Writer, runner *providerclient.Runner, do Check) error {
	bus := events.NewBus(time.Now)
	bus.Attach(&plainSink{plainReporter: plainReporter{present: present, w: w}})
	defer bus.Close()
	_, run, err := bus.Begin(context.Background(), "", "")
	if err != nil {
		return err
	}
	return check(run, runner, do)
}

func check(run *events.Run, runner *providerclient.Runner, do Check) error {
	scope := run.Phase(progressv1.Phase_PHASE_CHECK)
	err := do(scope, runner.Provider(scope))
	scope.End(err)
	return err
}

type plainSink struct {
	plainReporter

	mu      sync.Mutex
	spinner *Spinner
}

func (p *plainSink) Receive(ev *streamv1.RunEvent) {
	switch {
	case ev.GetLevel() == progressv1.Level_LEVEL_DEBUG:
	case ev.GetIdentity() != nil:
		p.Identity(ev.GetIdentity())
	case ev.GetStarted() != nil && ev.GetMessage() != "":
		p.spin(StartSpinner(p.present, p.w, ev.GetMessage()))
	case ev.GetEnded() != nil:
		p.spin(nil)
	case ev.GetLevel() == progressv1.Level_LEVEL_WARN && (ev.GetBody() == nil || ev.GetOutput() != nil):
		p.Warning(ev.GetMessage())
	case ev.GetBody() == nil || ev.GetOutput() != nil:
		p.Diagnostic(ev.GetMessage())
	}
}

func (p *plainSink) spin(next *Spinner) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.spinner != nil {
		p.spinner.Stop()
	}
	p.spinner = next
}

func (p *plainSink) Close() error {
	p.spin(nil)
	return nil
}
