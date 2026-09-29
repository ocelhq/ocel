package terminal

import (
	"encoding/hex"
	"fmt"
	"maps"
	"slices"
	"time"

	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
)

type phaseTally struct {
	since       time.Time
	units, done int
	overlapped  bool
}

func (t *phaseTally) finished() string {
	if !t.overlapped {
		return ""
	}
	return fmt.Sprintf(" (%d/%d)", t.done, t.units)
}

type spanTree[U any] struct {
	units   map[string]*U
	opened  map[string]*streamv1.RunEvent
	owners  map[string]string
	running []string
	tallies map[progressv1.Phase]*phaseTally
}

func newSpanTree[U any]() spanTree[U] {
	return spanTree[U]{
		units:   make(map[string]*U),
		opened:  make(map[string]*streamv1.RunEvent),
		owners:  make(map[string]string),
		tallies: make(map[progressv1.Phase]*phaseTally),
	}
}

func (t *spanTree[U]) joins(ev *streamv1.RunEvent) bool {
	return t.owners[spanKey(ev.GetStarted().GetParentSpanId())] != ""
}

func (t *spanTree[U]) open(span string, ev *streamv1.RunEvent, since time.Time, unit *U) {
	if t.joins(ev) {
		t.owners[span] = t.owners[spanKey(ev.GetStarted().GetParentSpanId())]
		return
	}
	tally := t.tallies[ev.GetPhase()]
	if tally == nil {
		tally = &phaseTally{since: since}
		t.tallies[ev.GetPhase()] = tally
	}
	if isPhaseSpan(ev) {
		return
	}
	tally.overlapped = tally.overlapped || tally.units > tally.done
	tally.units++
	t.units[span] = unit
	t.opened[span] = ev
	t.owners[span] = span
	t.running = append(t.running, span)
}

func (t *spanTree[U]) close(span string) (*U, *streamv1.RunEvent, *phaseTally) {
	unit, ok := t.units[span]
	if !ok {
		return nil, nil, nil
	}
	opened := t.opened[span]
	tally := t.tallies[opened.GetPhase()]
	tally.done++
	delete(t.units, span)
	delete(t.opened, span)
	t.running = slices.DeleteFunc(t.running, func(id string) bool { return id == span })
	maps.DeleteFunc(t.owners, func(_, owner string) bool { return owner == span })
	return unit, opened, tally
}

func (t *spanTree[U]) ownerOf(span string) *U {
	return t.units[t.owners[span]]
}

func spanKey(id []byte) string {
	if len(id) == 0 {
		return ""
	}
	return hex.EncodeToString(id)
}

func isPhaseSpan(ev *streamv1.RunEvent) bool {
	return len(ev.GetStarted().GetParentSpanId()) == 0 && ev.GetSubject() == "" && ev.GetMessage() == ""
}

func isTraceOnly(ev *streamv1.RunEvent) bool {
	return ev.GetLevel() == progressv1.Level_LEVEL_DEBUG && (ev.GetStarted() != nil || ev.GetEnded() != nil)
}
