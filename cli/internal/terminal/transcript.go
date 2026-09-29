package terminal

import (
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
	"time"

	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
)

const verbatimIndent = "    "

type Transcript struct {
	w            io.Writer
	present      Presentation
	verbatimText func(string) string

	mu            sync.Mutex
	spans         spanTree[spanBlock]
	verbatim      bool
	failed        bool
	nextHeartbeat time.Time
	shownForEvent bool
	held          bool
	wrote         bool
	blank         bool
	waitingOnPage bool

	stopHeartbeats    func()
	stopTicks         func()
	heartbeatsStopped chan struct{}
}

type blockLine struct {
	text    string
	raw     bool
	command bool
	from    line
}

type spanBlock struct {
	opened    *streamv1.RunEvent
	body      []blockLine
	resources resourceTally
}

func NewTranscript(w io.Writer, present Presentation) *Transcript {
	ticker := time.NewTicker(time.Second)
	s := newTranscript(w, present, ticker.C)
	s.stopTicks = ticker.Stop
	return s
}

func newTranscript(w io.Writer, present Presentation, ticks <-chan time.Time) *Transcript {
	stop := make(chan struct{})
	s := &Transcript{
		w:                 w,
		present:           present,
		spans:             newSpanTree[spanBlock](),
		stopHeartbeats:    sync.OnceFunc(func() { close(stop) }),
		stopTicks:         func() {},
		heartbeatsStopped: make(chan struct{}),
	}
	go s.runHeartbeats(ticks, stop)
	return s
}

func (s *Transcript) Receive(ev *streamv1.RunEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.shownForEvent = false
	defer s.postponeHeartbeat(ev)
	span := spanKey(ev.GetOperation().GetSpanId())
	switch {
	case isTraceOnly(ev):
	case ev.GetOperation().GetStarted() != nil:
		s.spans.open(span, ev, ev.GetOperation().GetTime().AsTime(), &spanBlock{opened: ev})
	case ev.GetOperation().GetEnded() != nil:
		s.end(span, ev)
	case ev.GetWaiting() != nil:
		s.held = true
		s.wait(ev.GetWaiting())
	case ev.GetResumed() != nil:
		s.held = false
		s.shownForEvent = true
		s.resume(ev)
	case ev.GetIdentity() != nil:
		s.printBlock(identityLines(s.present, ev.GetIdentity()))
	case ev.GetOperation().GetPlan() != nil:
		s.printBlock(planLines(s.present, ev.GetOperation().GetPlan()))
	case ev.GetOperation().GetDnsManualRecords() != nil:
		s.dnsRecords(ev)
	case ev.GetSummary() != nil:
		s.printSummary(ev)
	case ev.GetOperation().GetLevel() == progressv1.Level_LEVEL_DEBUG && !s.present.Verbose:
	case ev.GetOperation().GetOutput() != nil:
		s.within(span, blockLine{text: ev.GetOperation().GetMessage(), raw: true}, blockLine{text: ev.GetOperation().GetMessage(), raw: true})
	case ev.GetOperation().GetBody() == nil && ev.GetCli() == nil && ev.GetOperation().GetMessage() != "":
		s.within(span, s.detail(ev), s.standalone(ev))
	}
}

func (s *Transcript) within(span string, inSpan, standalone blockLine) {
	if tracked := s.spans.ownerOf(span); tracked != nil {
		tracked.body = append(tracked.body, inSpan)
		return
	}
	s.print(standalone)
}

func (s *Transcript) standalone(ev *streamv1.RunEvent) blockLine {
	return blockLine{text: lineOf(ev).render(s.present), from: lineOf(ev)}
}

func (s *Transcript) detail(ev *streamv1.RunEvent) blockLine {
	text := ev.GetOperation().GetMessage()
	if ev.GetOperation().GetLevel() == progressv1.Level_LEVEL_INFO {
		text = s.present.palette().Muted(text)
	}
	text = strings.ReplaceAll(text, "\n", "\n"+continuationIndent)
	if ev.GetOperation().GetLevel() != progressv1.Level_LEVEL_INFO {
		text = levelLabels[ev.GetOperation().GetLevel()].render(s.present) + " " + text
	}
	return blockLine{text: continuationIndent + text, from: lineOf(ev)}
}

func (s *Transcript) end(span string, ev *streamv1.RunEvent) {
	tracked, _, tally := s.spans.close(span)
	if tracked == nil {
		s.endResource(span, ev)
		return
	}
	ended := ev.GetOperation().GetEnded()
	took := formatDuration(endedDuration(ev, ended))
	failed := ended.GetStatus() == progressv1.SpanStatus_SPAN_STATUS_ERROR
	partial := !failed && ev.GetOperation().GetLevel() >= progressv1.Level_LEVEL_WARN
	head := line{
		level:   ev.GetOperation().GetLevel(),
		status:  ended.GetStatus(),
		message: ended.GetTitle(),
		timing:  " in " + took + tally.finished(),
		outcome: tracked.resources.summary(),
	}
	if failed {
		head.message, head.timing = tracked.opened.GetOperation().GetMessage()+" failed", " after "+took+tally.finished()
		if reason := ev.GetOperation().GetMessage(); reason != "" {
			head.outcome += ": " + reason
		}
	}
	if partial {
		head.status = progressv1.SpanStatus_SPAN_STATUS_UNSPECIFIED
	}
	header := tracked.header(head, s.present)
	if s.present.GitHubActions && !failed && !partial && len(tracked.body) > 0 {
		s.printGroup(header, tracked.body)
		return
	}
	s.print(header)
	if failed || !s.failed {
		s.print(tracked.body...)
	} else {
		s.print(slices.DeleteFunc(slices.Clone(tracked.body), func(l blockLine) bool {
			return l.raw || l.from.level < progressv1.Level_LEVEL_WARN
		})...)
	}
	s.failed = s.failed || failed
}

func (s *Transcript) endResource(span string, ev *streamv1.RunEvent) {
	tracked := s.spans.ownerOf(span)
	change, ok := resourceChangeOf(ev)
	if tracked == nil || !ok {
		return
	}
	tracked.resources.count(change)
	tracked.body = append(tracked.body, blockLine{text: continuationIndent + change.render(s.present)})
}

func (s *Transcript) dnsRecords(ev *streamv1.RunEvent) {
	records := ev.GetOperation().GetDnsManualRecords()
	if len(records.GetRecords()) == 0 {
		return
	}
	head := lineOf(ev)
	head.message = dnsHeadline(records.GetHeadline(), records.GetRecords())
	s.gap()
	s.print(blockLine{text: head.render(s.present), from: head})
	s.printBlock(dnsTable(records, s.present.Width))
}

func (s *Transcript) wait(waiting *streamv1.WaitingEvent) {
	if waiting.GetMissing() == nil && waiting.GetUrl() == "" {
		return
	}
	s.waitingOnPage = true
	lines := MissingVariablesLines(waiting.GetMissing(), s.present)
	s.printBlock(append(lines,
		"",
		blockIndent+"Fill them in at:",
		"",
		blockIndent+blockIndent+waiting.GetUrl(),
		"",
		blockIndent+"Waiting for the page — press Ctrl-C to abort. Nothing has been provisioned.",
	))
}

func (s *Transcript) resume(ev *streamv1.RunEvent) {
	if !s.waitingOnPage {
		return
	}
	s.waitingOnPage = false
	resumed := lineOf(ev)
	resumed.message = "Resumed — " + ev.GetResumed().GetReason()
	s.print(blockLine{text: resumed.render(s.present), from: resumed})
}

func (s *Transcript) printBlock(lines []string) {
	if len(lines) == 0 {
		return
	}
	s.gap()
	for _, text := range lines {
		s.print(blockLine{text: text})
	}
	s.gap()
}

func (s *Transcript) gap() {
	if s.wrote && !s.blank {
		fmt.Fprintln(s.w)
		s.blank = true
	}
}

func (s *Transcript) print(lines ...blockLine) {
	for _, l := range lines {
		s.shownForEvent = true
		if l.raw != s.verbatim {
			s.gap()
		}
		s.wrote = true
		s.blank = l.text == "" && !l.raw
		s.verbatim = l.raw
		if l.raw {
			text := l.text
			if s.verbatimText != nil {
				text = s.verbatimText(text)
			}
			fmt.Fprintln(s.w, verbatimIndent+text)
			continue
		}
		fmt.Fprintln(s.w, s.escapeWorkflowCommands(l))
		s.annotate(l.from.level, l.from.annotation())
	}
}

func lineOf(ev *streamv1.RunEvent) line {
	return line{level: ev.GetOperation().GetLevel(), phase: ev.GetOperation().GetPhase(), subject: ev.GetOperation().GetSubject(), message: ev.GetOperation().GetMessage()}
}

func (u *spanBlock) header(ended line, present Presentation) blockLine {
	ended.phase, ended.subject, ended.header = u.opened.GetOperation().GetPhase(), u.opened.GetOperation().GetSubject(), true
	return blockLine{text: ended.render(present), from: ended}
}

func (s *Transcript) Close() error {
	s.stopTicks()
	s.stopHeartbeats()
	<-s.heartbeatsStopped
	s.mu.Lock()
	defer s.mu.Unlock()
	s.printUnfinished()
	if s.verbatim {
		s.gap()
		s.verbatim = false
	}
	return nil
}

func (s *Transcript) printUnfinished() {
	for _, span := range slices.Clone(s.spans.running) {
		tracked, _, _ := s.spans.close(span)
		s.print(tracked.header(line{level: progressv1.Level_LEVEL_WARN, message: tracked.opened.GetOperation().GetMessage() + " did not finish"}, s.present))
		s.print(tracked.body...)
	}
}

func endedDuration(ev *streamv1.RunEvent, ended *progressv1.Ended) time.Duration {
	if ev.GetOperation().GetTime() == nil {
		return 0
	}
	return elapsed(ended.GetStartTimeUnixNano(), ev.GetOperation().GetTime().AsTime().UnixNano())
}

func elapsed(start, end int64) time.Duration {
	if start <= 0 || end <= start {
		return 0
	}
	return time.Duration(end - start)
}

func formatDuration(d time.Duration) string {
	if d <= 0 {
		return "0s"
	}
	if d < 500*time.Millisecond {
		return "<1s"
	}
	rounded := d.Round(time.Second)
	if rounded < time.Minute {
		return fmt.Sprintf("%ds", int(rounded/time.Second))
	}
	m := int(rounded / time.Minute)
	sec := int((rounded % time.Minute) / time.Second)
	return fmt.Sprintf("%dm%02ds", m, sec)
}
