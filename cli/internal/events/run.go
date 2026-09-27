package events

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/ocelhq/ocel/cli/internal/envgate"
	"github.com/ocelhq/ocel/cli/internal/exitsig"
	"github.com/ocelhq/ocel/cli/internal/runtrace"
	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
)

type Run struct {
	ctx     context.Context
	bus     *Bus
	command string
	start   time.Time
	trace   *runtrace.Run

	mu     sync.Mutex
	phases map[progressv1.Phase]*Scope
	open   []*Scope
	holds  int
	apps   []*progressv1.AppResult

	endOnce sync.Once
}

func (r *Run) End(errp *error) {
	r.endOnce.Do(func() {
		err := *errp
		for _, s := range slices.Backward(r.children(nil)) {
			s.End(err)
		}
		result, code := r.result(err)
		r.mu.Lock()
		result.Apps = r.apps
		r.mu.Unlock()
		result.DurationMs = r.bus.now().Sub(r.start).Milliseconds()
		if r.trace != nil {
			result.LogPath = r.trace.LogPath()
		}
		r.bus.Send(&streamv1.RunEvent{Level: resultLevel(result), Body: &streamv1.RunEvent_Result{Result: result}})
		if r.trace != nil {
			r.bus.detach(r.trace)
			_ = r.trace.Close()
		}
		if code != 0 {
			*errp = &exitsig.ExitError{Code: code}
		}
	})
}

func (r *Run) result(err error) (*streamv1.RunResultEvent, int) {
	switch {
	case err == nil:
		return &streamv1.RunResultEvent{Success: true}, 0
	case r.ctx.Err() != nil:
		note := "Resources may be partially created."
		if r.held() {
			note = "Nothing has been provisioned."
		}
		return &streamv1.RunResultEvent{
			Interrupted: true,
			Headline:    "Cancelled",
			Detail:      fmt.Sprintf("%s\nRe-run `%s` to reconcile.", note, r.command),
		}, exitsig.InterruptCode
	}
	result := &streamv1.RunResultEvent{Detail: err.Error()}
	var refusal *envgate.Refusal
	if errors.As(err, &refusal) {
		result.Missing = refusal.Missing()
		result.Detail = strings.TrimLeft(strings.TrimPrefix(err.Error(), refusal.Error()), "\n")
	}
	return result, 1
}

func resultLevel(result *streamv1.RunResultEvent) progressv1.Level {
	switch {
	case result.GetInterrupted():
		return progressv1.Level_LEVEL_WARN
	case !result.GetSuccess():
		return progressv1.Level_LEVEL_ERROR
	}
	return progressv1.Level_LEVEL_INFO
}

func (r *Run) Phase(phase progressv1.Phase) *Scope {
	r.mu.Lock()
	defer r.mu.Unlock()
	if s, ok := r.phases[phase]; ok {
		return s
	}
	s := r.beginLocked(phase, nil, "", "")
	r.phases[phase] = s
	return s
}

func (r *Run) begin(parent *Scope, subject, message string) *Scope {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.beginLocked(parent.phase, parent, subject, message)
}

func (r *Run) beginLocked(phase progressv1.Phase, parent *Scope, subject, message string) *Scope {
	s := &Scope{run: r, parent: parent, phase: phase, subject: subject, spanID: newSpanID(), start: r.bus.now()}
	var parentID []byte
	if parent != nil {
		parentID = parent.spanID
	}
	r.open = append(r.open, s)
	r.bus.Send(&streamv1.RunEvent{
		Time:    timestamppb.New(s.start),
		Phase:   phase,
		Subject: subject,
		Message: message,
		SpanId:  s.spanID,
		Body:    &streamv1.RunEvent_Started{Started: &progressv1.Started{ParentSpanId: parentID}},
	})
	return s
}

func (r *Run) close(s *Scope) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.phases[s.phase] == s {
		delete(r.phases, s.phase)
	}
	r.open = slices.DeleteFunc(r.open, func(o *Scope) bool { return o == s })
}

func (r *Run) children(parent *Scope) []*Scope {
	r.mu.Lock()
	defer r.mu.Unlock()
	var children []*Scope
	for _, s := range r.open {
		if s.parent == parent {
			children = append(children, s)
		}
	}
	return children
}

func (r *Run) failureLevel() progressv1.Level {
	if r.ctx.Err() != nil {
		return progressv1.Level_LEVEL_WARN
	}
	return progressv1.Level_LEVEL_ERROR
}

func (r *Run) held() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.holds > 0
}

func (r *Run) hold(delta int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.holds += delta
}

func (r *Run) record(apps []*progressv1.AppResult) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.apps = apps
}
