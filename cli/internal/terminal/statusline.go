package terminal

import (
	"cmp"
	"fmt"
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

type statusLine struct {
	now       func() time.Time
	present   Presentation
	startedAt time.Time

	phase      progressv1.Phase
	spans      spanTree[statusUnit]
	shown      string
	shownAt    time.Time
	lastActive string
}

type statusUnit struct {
	opened   *streamv1.RunEvent
	output   string
	progress string
}

func (u *statusUnit) latest() string {
	return cmp.Or(u.output, u.progress)
}

func newStatusLine(now func() time.Time, present Presentation) *statusLine {
	return &statusLine{
		now:     now,
		present: present,
		spans:   newSpanTree[statusUnit](),
	}
}

func (l *statusLine) observe(ev *streamv1.RunEvent) {
	at := l.now()
	if l.startedAt.IsZero() {
		l.startedAt = at
	}
	span := spanKey(ev.GetOperation().GetSpanId())
	switch {
	case isTraceOnly(ev):
	case ev.GetOperation().GetStarted() != nil:
		l.open(span, ev, at)
	case ev.GetOperation().GetEnded() != nil:
		l.end(span)
	case ev.GetOperation().GetLevel() == progressv1.Level_LEVEL_DEBUG:
	case ev.GetOperation().GetOutput() != nil:
		if text, ok := sanitize(ev.GetOperation().GetMessage()); ok {
			l.record(span, at, func(u *statusUnit) { u.output = text })
		}
	case ev.GetOperation().GetBody() == nil && ev.GetCli() == nil:
		first, _, _ := strings.Cut(ev.GetOperation().GetMessage(), "\n")
		if text, ok := sanitize(first); ok {
			l.record(span, at, func(u *statusUnit) { u.progress = text })
		}
	}
}

func (l *statusLine) open(span string, ev *streamv1.RunEvent, at time.Time) {
	if !l.spans.joins(ev) {
		l.phase = ev.GetOperation().GetPhase()
	}
	l.spans.open(span, ev, at, &statusUnit{opened: ev})
}

func (l *statusLine) end(span string) {
	if unit, _, _ := l.spans.close(span); unit != nil && l.shown == span {
		l.shown = ""
	}
}

func (l *statusLine) record(span string, at time.Time, update func(*statusUnit)) {
	unit := l.spans.ownerOf(span)
	if unit == nil {
		return
	}
	update(unit)
	l.lastActive = l.spans.owners[span]
	l.pick(at)
}

func (l *statusLine) pick(at time.Time) {
	next := l.lastActive
	if l.spans.units[next] == nil && len(l.spans.running) > 0 {
		next = l.spans.running[len(l.spans.running)-1]
	}
	if shown := l.spans.units[l.shown]; shown != nil && (next == l.shown || l.spans.units[l.lastActive] == nil ||
		shown.latest() != "" && at.Sub(l.shownAt) < liveHold) {
		return
	}
	l.shown, l.shownAt = next, at
}

func (l *statusLine) render(width int) string {
	tally := l.spans.tallies[l.phase]
	if tally == nil {
		return ""
	}
	at := l.now()
	l.pick(at)
	head := continuationIndent
	if tag, ok := phaseTag(l.present, l.phase); ok {
		head += tag + " "
	}
	head += l.present.palette().Accent(spinnerFrame(int(at.Sub(l.startedAt) / frameRate)))
	spinnerEnds := displayWidth(head)
	if unit := l.spans.units[l.shown]; unit != nil {
		head += " " + l.naming(unit)
	}
	took := formatDuration(at.Sub(tally.since))
	if tally.units > 0 {
		took = fmt.Sprintf("%d/%d · %s", tally.done, tally.units, took)
	}
	room := width - 1 - displayWidth(took) - len(liveGutter)
	if room < spinnerEnds {
		if width-1 < spinnerEnds {
			return ""
		}
		return fitToWidth(head, width-1)
	}
	head = fitToWidth(head, room)
	return head + strings.Repeat(" ", max(room-displayWidth(head), 0)) + liveGutter + l.present.palette().Muted(took)
}

func (l *statusLine) naming(unit *statusUnit) string {
	said := unit.latest()
	subject := unit.opened.GetOperation().GetSubject()
	if subject == "" {
		named := unit.opened.GetOperation().GetMessage()
		if said != "" {
			named += liveGutter + l.present.palette().Muted(said)
		}
		return named
	}
	if said == "" {
		title, _, _ := strings.Cut(unit.opened.GetOperation().GetMessage(), "\n")
		said, _ = sanitize(title)
	}
	named := l.present.palette().Bold(subject) + ":"
	if said != "" {
		named += " " + l.present.palette().Muted(said)
	}
	return named
}
