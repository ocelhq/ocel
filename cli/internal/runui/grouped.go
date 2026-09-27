package runui

import (
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"
	"sync"

	"google.golang.org/protobuf/proto"

	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
)

const verbatimIndent = "    "

type GroupedSink struct {
	w       io.Writer
	present Presentation

	mu       sync.Mutex
	units    map[string]*unitBlock
	started  []string
	owners   map[string]string
	verbatim bool
	failed   bool
}

type unitBlock struct {
	opened *streamv1.RunEvent
	body   []blockLine
}

func NewGroupedSink(w io.Writer, present Presentation) *GroupedSink {
	return &GroupedSink{
		w:       w,
		present: present,
		units:   make(map[string]*unitBlock),
		owners:  make(map[string]string),
	}
}

func (s *GroupedSink) Receive(ev *streamv1.RunEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	span := stageKey(ev.GetSpanId())
	switch {
	case ev.GetStarted() != nil:
		s.open(span, ev)
	case ev.GetEnded() != nil:
		s.end(span, ev)
	case ev.GetLevel() == progressv1.Level_LEVEL_DEBUG && !s.present.Verbose:
	case ev.GetOutput() != nil:
		s.within(span, blockLine{text: ev.GetMessage(), raw: true}, blockLine{text: ev.GetMessage(), raw: true})
	case ev.GetCounter() != nil:
		counted := proto.CloneOf(ev)
		counted.Message = progressLogLine(ev.GetMessage(), ev.GetCounter().GetCurrent(), ev.GetCounter().Total)
		s.within(span, s.detail(counted), blockLine{text: lineOf(counted).render(s.present)})
	case ev.GetBody() == nil && ev.GetMessage() != "":
		s.within(span, s.detail(ev), blockLine{text: lineOf(ev).render(s.present)})
	}
}

func (s *GroupedSink) within(span string, inUnit, alone blockLine) {
	if unit := s.units[s.owners[span]]; unit != nil {
		unit.body = append(unit.body, inUnit)
		return
	}
	s.print(alone)
}

func (s *GroupedSink) detail(ev *streamv1.RunEvent) blockLine {
	text := strings.ReplaceAll(ev.GetMessage(), "\n", "\n"+continuationIndent)
	if ev.GetLevel() != progressv1.Level_LEVEL_INFO {
		text = levelLabels[ev.GetLevel()].render(s.present) + " " + text
	}
	return blockLine{text: continuationIndent + text}
}

func (s *GroupedSink) open(span string, ev *streamv1.RunEvent) {
	parent := stageKey(ev.GetStarted().GetParentSpanId())
	switch {
	case parent == "" && ev.GetSubject() == "" && ev.GetMessage() == "":
	case s.owners[parent] != "":
		s.owners[span] = s.owners[parent]
	default:
		s.units[span] = &unitBlock{opened: ev}
		s.started = append(s.started, span)
		s.owners[span] = span
	}
}

func (s *GroupedSink) end(span string, ev *streamv1.RunEvent) {
	unit := s.units[span]
	if unit == nil {
		return
	}
	s.forget(span)
	ended := ev.GetEnded()
	took := formatDuration(endedDuration(ev, ended))
	failed := ended.GetStatus() == progressv1.SpanStatus_SPAN_STATUS_ERROR
	message := fmt.Sprintf("%s in %s", unit.opened.GetMessage(), took)
	if failed {
		message = fmt.Sprintf("%s failed after %s", unit.opened.GetMessage(), took)
		if reason := ev.GetMessage(); reason != "" {
			message += ": " + reason
		}
	}
	s.print(unit.header(ev.GetLevel(), ended.GetStatus(), message, s.present))
	if failed || !s.failed {
		s.print(unit.body...)
	}
	s.failed = s.failed || failed
}

func (s *GroupedSink) print(lines ...blockLine) {
	for _, l := range lines {
		if l.raw != s.verbatim {
			fmt.Fprintln(s.w)
		}
		s.verbatim = l.raw
		if l.raw {
			fmt.Fprintln(s.w, verbatimIndent+l.text)
			continue
		}
		fmt.Fprintln(s.w, l.text)
	}
}

func lineOf(ev *streamv1.RunEvent) line {
	return line{level: ev.GetLevel(), phase: ev.GetPhase(), subject: ev.GetSubject(), message: ev.GetMessage()}
}

func (s *GroupedSink) forget(span string) {
	delete(s.units, span)
	s.started = slices.DeleteFunc(s.started, func(id string) bool { return id == span })
	maps.DeleteFunc(s.owners, func(_, owner string) bool { return owner == span })
}

func (u *unitBlock) header(level progressv1.Level, ends progressv1.SpanStatus, message string, present Presentation) blockLine {
	head := lineOf(u.opened)
	head.level, head.ends, head.message = level, ends, message
	return blockLine{text: head.render(present)}
}

func (s *GroupedSink) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, span := range slices.Clone(s.started) {
		unit := s.units[span]
		s.forget(span)
		s.print(unit.header(progressv1.Level_LEVEL_WARN, progressv1.SpanStatus_SPAN_STATUS_UNSPECIFIED, unit.opened.GetMessage()+" did not finish", s.present))
		s.print(unit.body...)
	}
	if s.verbatim {
		fmt.Fprintln(s.w)
		s.verbatim = false
	}
	return nil
}
