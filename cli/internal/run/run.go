package run

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/ocelhq/ocel/cli/internal/exitcode"
	"github.com/ocelhq/ocel/cli/internal/runtrace"
	"github.com/ocelhq/ocel/pkg/progress"
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
	trace   *runtrace.Trace

	mu          sync.Mutex
	phases      map[progressv1.Phase]*Span
	open        []*Span
	changed     bool
	apps        []*progressv1.AppResult
	identity    *streamv1.IdentityEvent
	promotion   string
	urlNotes    []string
	propagation *progressv1.Propagation
	headline    string

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
		result.Apps, result.ChangeStarted, result.PromotionId = r.apps, r.changed, r.promotion
		result.Tier, result.Origin = r.identity.GetTier(), r.identity.GetOrigin()
		r.mu.Unlock()
		result.DurationMs = r.bus.now().Sub(r.start).Milliseconds()
		if r.trace != nil {
			result.LogPath = r.trace.LogPath()
		}
		r.bus.send(&streamv1.RunEvent{Level: resultLevel(result), Body: &streamv1.RunEvent_Summary{Summary: result}})
		if r.trace != nil {
			r.bus.detach(r.trace)
			_ = r.trace.Close()
		}
		r.bus.finish(r)
		if code != 0 {
			*errp = &exitcode.ExitError{Code: code}
		}
	})
}

func (r *Run) interrupt() {
	err := context.Canceled
	r.End(&err)
}

func (r *Run) result(err error) (*streamv1.RunSummary, int) {
	switch {
	case err == nil:
		r.mu.Lock()
		defer r.mu.Unlock()
		return &streamv1.RunSummary{
			Success:     true,
			Headline:    cmp.Or(r.headline, r.verdict("finished")),
			UrlNotes:    r.urlNotes,
			Propagation: r.propagation,
		}, 0
	case r.ctx.Err() != nil:
		result := &streamv1.RunSummary{Interrupted: true, Headline: r.verdict("cancelled")}
		if r.mayHaveChanged() {
			result.Detail = fmt.Sprintf("Resources may be partially created.\nRe-run `%s` to reconcile.", r.command)
		}
		return result, exitcode.Interrupt
	}
	result := &streamv1.RunSummary{Headline: r.verdict("failed"), Detail: err.Error()}
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

func (r *Run) Succeed(headline string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.headline = headline
}

func resultLevel(result *streamv1.RunSummary) progressv1.Level {
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

func (r *Run) Ask(ask func() error) error {
	return askHolding(r.Hold, ask)
}

func askHolding(hold func(*streamv1.WaitingEvent) func(reason string), ask func() error) error {
	resume := hold(&streamv1.WaitingEvent{})
	defer resume("answered")
	return ask()
}

func (r *Run) holdOn(onSpan func(*streamv1.RunEvent) *streamv1.RunEvent, waiting *streamv1.WaitingEvent) (resume func(reason string)) {
	r.bus.send(onSpan(&streamv1.RunEvent{Body: &streamv1.RunEvent_Waiting{Waiting: waiting}}))
	var once sync.Once
	return func(reason string) {
		once.Do(func() {
			r.bus.send(onSpan(&streamv1.RunEvent{Body: &streamv1.RunEvent_Resumed{
				Resumed: &streamv1.ResumedEvent{Reason: reason},
			}}))
		})
	}
}

func (r *Run) Phase(phase progressv1.Phase) *Span {
	r.mu.Lock()
	defer r.mu.Unlock()
	if s, ok := r.phases[phase]; ok {
		return s
	}
	s := r.beginLocked(phase, nil, "", progress.Title{})
	r.phases[phase] = s
	return s
}

func (r *Run) begin(parent *Span, subject string, title progress.Title) *Span {
	return r.beginAt(parent, subject, title, r.bus.now())
}

func (r *Run) beginAt(parent *Span, subject string, title progress.Title, start time.Time) *Span {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.beginLockedAt(parent.phase, parent, subject, title, start)
}

func (r *Run) beginLocked(phase progressv1.Phase, parent *Span, subject string, title progress.Title) *Span {
	return r.beginLockedAt(phase, parent, subject, title, r.bus.now())
}

func (r *Run) beginLockedAt(phase progressv1.Phase, parent *Span, subject string, title progress.Title, start time.Time) *Span {
	return r.openLocked(&Span{run: r, parent: parent, phase: phase, subject: subject, spanID: newSpanID(), start: start, title: title, level: progressv1.Level_LEVEL_INFO})
}

func (r *Run) openLocked(s *Span) *Span {
	r.enterLocked(s.phase)
	var parentID []byte
	if s.parent != nil {
		parentID = s.parent.spanID
	}
	r.open = append(r.open, s)
	r.bus.send(&streamv1.RunEvent{
		Time:    timestamppb.New(s.start),
		Level:   s.level,
		Phase:   s.phase,
		Subject: s.subject,
		Message: s.title.Started,
		SpanId:  s.spanID,
		Body:    &streamv1.RunEvent_Started{Started: &progressv1.Started{ParentSpanId: parentID}},
	})
	return s
}

func (r *Run) beginTrace(parent *Span, subject, name string, attrs []progress.Attr) *Span {
	r.mu.Lock()
	defer r.mu.Unlock()
	title := progress.Title{Started: name, Ended: name}
	s := &Span{run: r, parent: parent, phase: parent.phase, subject: subject, spanID: newSpanID(), start: r.bus.now(), title: title, level: progressv1.Level_LEVEL_DEBUG}
	s.SetAttributes(append([]progress.Attr{{Key: progress.AttrKeySpanName, Value: name}}, attrs...)...)
	return r.openLocked(s)
}

func (r *Run) close(s *Span) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.phases[s.phase] == s {
		delete(r.phases, s.phase)
	}
	r.open = slices.DeleteFunc(r.open, func(o *Span) bool { return o == s })
}

func (r *Run) stillOpen() []*Span {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.open)
}

func (r *Run) children(parent *Span) []*Span {
	r.mu.Lock()
	defer r.mu.Unlock()
	var children []*Span
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

func (r *Run) record(result *progressv1.OperationResult) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if apps := result.GetApps(); len(apps) > 0 {
		r.apps = apps
	}
	if promotion := result.GetPromotionId(); promotion != "" {
		r.promotion = promotion
	}
	if notes := result.GetUrlNotes(); len(notes) > 0 {
		r.urlNotes = notes
	}
	if propagation := result.GetPropagation(); propagation != nil {
		r.propagation = propagation
	}
}

func (r *Run) identify(identity *streamv1.IdentityEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.identity = identity
}
