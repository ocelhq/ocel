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

	"github.com/ocelhq/ocel/cli/internal/exitsig"
	"github.com/ocelhq/ocel/cli/internal/runtrace"
	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
)

type missingVariablesError interface {
	error
	Variables() *streamv1.MissingVariables
}

type Run struct {
	ctx     context.Context
	bus     *Bus
	command string
	start   time.Time
	trace   *runtrace.Run

	mu      sync.Mutex
	phases  map[progressv1.Phase]*Scope
	open    []*Scope
	changed bool
	apps    []*progressv1.AppResult
	success *streamv1.RunResultEvent

	endOnce sync.Once
}

func (r *Run) End(errp *error) {
	r.endOnce.Do(func() {
		err := *errp
		for _, s := range slices.Backward(r.stillOpen()) {
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
		r.bus.send(&streamv1.RunEvent{Level: resultLevel(result), Body: &streamv1.RunEvent_Result{Result: result}})
		if r.trace != nil {
			r.bus.detach(r.trace)
			_ = r.trace.Close()
		}
		r.bus.finish(r)
		if code != 0 {
			*errp = &exitsig.ExitError{Code: code}
		}
	})
}

func (r *Run) interrupt() {
	err := context.Canceled
	r.End(&err)
}

func (r *Run) result(err error) (*streamv1.RunResultEvent, int) {
	switch {
	case err == nil:
		r.mu.Lock()
		defer r.mu.Unlock()
		result := &streamv1.RunResultEvent{Success: true, Headline: r.verdict("finished")}
		if r.success != nil {
			result = r.success
		}
		return result, 0
	case r.ctx.Err() != nil:
		result := &streamv1.RunResultEvent{Interrupted: true, Headline: r.verdict("cancelled")}
		if r.mayHaveChanged() {
			result.Detail = fmt.Sprintf("Resources may be partially created.\nRe-run `%s` to reconcile.", r.command)
		}
		return result, exitsig.InterruptCode
	}
	result := &streamv1.RunResultEvent{Headline: r.verdict("failed"), Detail: err.Error()}
	var missing missingVariablesError
	if errors.As(err, &missing) {
		result.Missing = missing.Variables()
		result.Detail = strings.TrimLeft(strings.TrimPrefix(err.Error(), missing.Error()), "\n")
	}
	return result, 1
}

func (r *Run) verdict(outcome string) string {
	name := strings.TrimPrefix(r.command, "ocel ")
	if name == "" {
		return strings.ToUpper(outcome[:1]) + outcome[1:]
	}
	return strings.ToUpper(name[:1]) + name[1:] + " " + outcome
}

func (r *Run) Finish(headline string) {
	r.Deployed(headline, nil, nil)
}

func (r *Run) Deployed(headline string, urlNotes []string, flip *progressv1.FlipBound) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.success = &streamv1.RunResultEvent{Success: true, Headline: headline, UrlNotes: urlNotes, FlipBound: flip}
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

func (r *Run) Hold(waiting *streamv1.WaitingEvent) (resume func(reason string)) {
	return r.holdOn(func(ev *streamv1.RunEvent) *streamv1.RunEvent { return ev }, waiting)
}

func (r *Run) holdOn(scoped func(*streamv1.RunEvent) *streamv1.RunEvent, waiting *streamv1.WaitingEvent) (resume func(reason string)) {
	r.bus.send(scoped(&streamv1.RunEvent{Body: &streamv1.RunEvent_Waiting{Waiting: waiting}}))
	var once sync.Once
	return func(reason string) {
		once.Do(func() {
			r.bus.send(scoped(&streamv1.RunEvent{Body: &streamv1.RunEvent_Resumed{
				Resumed: &streamv1.ResumedEvent{Reason: reason},
			}}))
		})
	}
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
	return r.beginAt(parent, subject, message, r.bus.now())
}

func (r *Run) beginAt(parent *Scope, subject, message string, start time.Time) *Scope {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.beginLockedAt(parent.phase, parent, subject, message, start)
}

func (r *Run) beginLocked(phase progressv1.Phase, parent *Scope, subject, message string) *Scope {
	return r.beginLockedAt(phase, parent, subject, message, r.bus.now())
}

func (r *Run) beginLockedAt(phase progressv1.Phase, parent *Scope, subject, message string, start time.Time) *Scope {
	s := &Scope{run: r, parent: parent, phase: phase, subject: subject, spanID: newSpanID(), start: start}
	r.enterLocked(phase)
	var parentID []byte
	if parent != nil {
		parentID = parent.spanID
	}
	r.open = append(r.open, s)
	r.bus.send(&streamv1.RunEvent{
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

func (r *Run) stillOpen() []*Scope {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.open)
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

var changingPhases = map[progressv1.Phase]bool{
	progressv1.Phase_PHASE_PROVISION: true,
	progressv1.Phase_PHASE_DEPLOY:    true,
	progressv1.Phase_PHASE_PROMOTE:   true,
	progressv1.Phase_PHASE_DESTROY:   true,
}

func IsChanging(phase progressv1.Phase) bool { return changingPhases[phase] }

func (r *Run) enter(phase progressv1.Phase) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.enterLocked(phase)
}

func (r *Run) enterLocked(phase progressv1.Phase) {
	r.changed = r.changed || changingPhases[phase]
}

func (r *Run) mayHaveChanged() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.changed
}

func (r *Run) record(apps []*progressv1.AppResult) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.apps = apps
}
