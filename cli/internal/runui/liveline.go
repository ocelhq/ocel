package runui

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
	"time"

	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
)

const (
	liveGutter = "  "
	liveHold   = 1500 * time.Millisecond
	frameRate  = 100 * time.Millisecond
)

var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

func spinnerFrame(n int) string {
	return spinnerFrames[n%len(spinnerFrames)]
}

type liveLine struct {
	now    func() time.Time
	origin time.Time

	phase   progressv1.Phase
	tallies map[progressv1.Phase]*phaseTally
	units   map[string]*liveUnit
	owners  map[string]string
	running []string
	shown   string
	shownAt time.Time
	spoke   string
}

type liveUnit struct {
	opened   *streamv1.RunEvent
	output   string
	progress string
}

func (u *liveUnit) said() string {
	return cmp.Or(u.output, u.progress)
}

func newLiveLine(now func() time.Time) *liveLine {
	return &liveLine{
		now:     now,
		tallies: make(map[progressv1.Phase]*phaseTally),
		units:   make(map[string]*liveUnit),
		owners:  make(map[string]string),
	}
}

func (l *liveLine) observe(ev *streamv1.RunEvent) {
	at := l.now()
	if l.origin.IsZero() {
		l.origin = at
	}
	span := stageKey(ev.GetSpanId())
	switch {
	case ev.GetStarted() != nil:
		l.open(span, ev, at)
	case ev.GetEnded() != nil:
		l.end(span)
	case ev.GetLevel() == progressv1.Level_LEVEL_DEBUG:
	case ev.GetOutput() != nil:
		if text, ok := sanitize(ev.GetMessage()); ok {
			l.speak(span, at, func(u *liveUnit) { u.output = text })
		}
	case ev.GetBody() == nil:
		first, _, _ := strings.Cut(ev.GetMessage(), "\n")
		if text, ok := sanitize(first); ok {
			l.speak(span, at, func(u *liveUnit) { u.progress = text })
		}
	}
}

func (l *liveLine) open(span string, ev *streamv1.RunEvent, at time.Time) {
	parent := stageKey(ev.GetStarted().GetParentSpanId())
	if l.owners[parent] != "" {
		l.owners[span] = l.owners[parent]
		return
	}
	l.phase = ev.GetPhase()
	tally := l.tallies[l.phase]
	if tally == nil {
		tally = &phaseTally{since: at}
		l.tallies[l.phase] = tally
	}
	if bareScope(ev) {
		return
	}
	l.units[span] = &liveUnit{opened: ev}
	l.owners[span] = span
	l.running = append(l.running, span)
	tally.units++
}

func (l *liveLine) end(span string) {
	unit := l.units[span]
	if unit == nil {
		return
	}
	l.tallies[unit.opened.GetPhase()].done++
	delete(l.units, span)
	l.running = slices.DeleteFunc(l.running, func(id string) bool { return id == span })
	for id, owner := range l.owners {
		if owner == span {
			delete(l.owners, id)
		}
	}
	if l.shown == span {
		l.shown = ""
	}
}

func (l *liveLine) speak(span string, at time.Time, say func(*liveUnit)) {
	owner := l.owners[span]
	unit := l.units[owner]
	if unit == nil {
		return
	}
	say(unit)
	l.spoke = owner
	l.pick(at)
}

func (l *liveLine) pick(at time.Time) {
	next := l.spoke
	if l.units[next] == nil && len(l.running) > 0 {
		next = l.running[len(l.running)-1]
	}
	if shown := l.units[l.shown]; shown != nil && (next == l.shown || l.units[l.spoke] == nil ||
		shown.said() != "" && at.Sub(l.shownAt) < liveHold) {
		return
	}
	l.shown, l.shownAt = next, at
}

func (l *liveLine) render(width int) string {
	tally := l.tallies[l.phase]
	if tally == nil {
		return ""
	}
	at := l.now()
	l.pick(at)
	head := spinnerFrame(int(at.Sub(l.origin) / frameRate))
	if name := phaseNames[l.phase]; name != "" {
		head += " [" + name + "]"
	}
	if tally.units > 0 {
		head += fmt.Sprintf(" %d/%d", tally.done, tally.units)
	}
	if unit := l.units[l.shown]; unit != nil {
		head += " · " + cmp.Or(unit.opened.GetSubject(), unit.opened.GetMessage())
		if said := unit.said(); said != "" {
			head += liveGutter + said
		}
	}
	took := formatDuration(at.Sub(tally.since))
	room := width - 1 - displayWidth(took) - len(liveGutter)
	if room < 1 {
		return fitToWidth(head, width-1)
	}
	head = fitToWidth(head, room)
	return head + strings.Repeat(" ", max(room-displayWidth(head), 0)) + liveGutter + took
}

func bareScope(ev *streamv1.RunEvent) bool {
	return len(ev.GetStarted().GetParentSpanId()) == 0 && ev.GetSubject() == "" && ev.GetMessage() == ""
}
