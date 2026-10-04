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
	spans      spanTree[statusSpan]
	shown      string
	shownAt    time.Time
	lastActive string
}

type statusSpan struct {
	opened   *streamv1.RunEvent
	output   string
	progress string
}

func (u *statusSpan) latest() string {
	return cmp.Or(u.output, u.progress)
}

func newStatusLine(now func() time.Time, present Presentation) *statusLine {
	return &statusLine{
		now:     now,
		present: present,
		spans:   newSpanTree[statusSpan](),
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
			l.record(span, at, func(u *statusSpan) { u.output = text })
		}
	case ev.GetOperation().GetBody() == nil && ev.GetCli() == nil:
		first, _, _ := strings.Cut(ev.GetOperation().GetMessage(), "\n")
		if text, ok := sanitize(first); ok {
			l.record(span, at, func(u *statusSpan) { u.progress = text })
		}
	}
}

func (l *statusLine) open(span string, ev *streamv1.RunEvent, at time.Time) {
	if !l.spans.joins(ev) {
		l.phase = ev.GetOperation().GetPhase()
	}
	l.spans.open(span, ev, at, &statusSpan{opened: ev})
}

func (l *statusLine) end(span string) {
	if tracked, _, _ := l.spans.close(span); tracked != nil && l.shown == span {
		l.shown = ""
	}
}

func (l *statusLine) record(span string, at time.Time, update func(*statusSpan)) {
	tracked := l.spans.ownerOf(span)
	if tracked == nil {
		return
	}
	update(tracked)
	l.lastActive = l.spans.owners[span]
	l.pick(at)
}

func (l *statusLine) pick(at time.Time) {
	next := l.lastActive
	if l.spans.tracked[next] == nil && len(l.spans.running) > 0 {
		next = l.spans.running[len(l.spans.running)-1]
	}
	if shown := l.spans.tracked[l.shown]; shown != nil && (next == l.shown || l.spans.tracked[l.lastActive] == nil ||
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
	head += l.present.Palette().Accent(spinnerFrame(int(at.Sub(l.startedAt) / frameRate)))
	spinnerEnds := displayWidth(head)
	if tracked := l.spans.tracked[l.shown]; tracked != nil {
		head += " " + l.naming(tracked)
	}
	took := formatDuration(at.Sub(tally.since))
	if tally.opened > 0 {
		took = fmt.Sprintf("%d/%d · %s", tally.done, tally.opened, took)
	}
	room := width - 1 - displayWidth(took) - len(liveGutter)
	if room < spinnerEnds {
		if width-1 < spinnerEnds {
			return ""
		}
		return fitToWidth(head, width-1)
	}
	head = fitToWidth(head, room)
	return head + strings.Repeat(" ", max(room-displayWidth(head), 0)) + liveGutter + l.present.Palette().Muted(took)
}

func (l *statusLine) naming(tracked *statusSpan) string {
	said := tracked.latest()
	subject := tracked.opened.GetOperation().GetSubject()
	if subject == "" {
		named := tracked.opened.GetOperation().GetMessage()
		if said != "" {
			named += liveGutter + l.present.Palette().Muted(said)
		}
		return named
	}
	if said == "" {
		title, _, _ := strings.Cut(tracked.opened.GetOperation().GetMessage(), "\n")
		said, _ = sanitize(title)
	}
	named := l.present.Palette().Bold(subject) + ":"
	if said != "" {
		named += " " + l.present.Palette().Muted(said)
	}
	return named
}
