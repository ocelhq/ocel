package runui

import (
	"cmp"
	"encoding/hex"
	"fmt"
	"io"
	"maps"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/ocelhq/ocel/cli/internal/events"

	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
)

const (
	verbatimIndent = "    "
	heartbeatAfter = 30 * time.Second
	heartbeatEvery = 60 * time.Second
)

var annotationCommands = map[progressv1.Level]string{
	progressv1.Level_LEVEL_WARN:  "warning",
	progressv1.Level_LEVEL_ERROR: "error",
}

var workflowData = strings.NewReplacer("%", "%25", "\r", "%0D", "\n", "%0A")

var workflowCommandStart = regexp.MustCompile(`(\A|[\r\n])([\t\v\f \x{85}\p{Z}]*)(::|##\[)`)

const commandBreak = "\u200b"

var phaseGerunds = map[progressv1.Phase]string{
	progressv1.Phase_PHASE_CHECK:     "checking",
	progressv1.Phase_PHASE_BUILD:     "building",
	progressv1.Phase_PHASE_PLAN:      "planning",
	progressv1.Phase_PHASE_PROVISION: "provisioning",
	progressv1.Phase_PHASE_DEPLOY:    "deploying",
	progressv1.Phase_PHASE_PROMOTE:   "promoting",
	progressv1.Phase_PHASE_DESTROY:   "destroying",
}

type GroupedSink struct {
	w            io.Writer
	present      Presentation
	verbatimText func(string) string

	mu            sync.Mutex
	units         map[string]*unitBlock
	started       []string
	owners        map[string]string
	verbatim      bool
	failed        bool
	tallies       map[progressv1.Phase]*phaseTally
	beatAt        time.Time
	silenceBroke  bool
	held          bool
	wrote         bool
	blank         bool
	gated         bool
	tier          environmentv1.Tier
	origin        *streamv1.Party
	promotion     string
	changeStarted bool

	stopBeats func()
	stopTicks func()
	beating   chan struct{}
}

type blockLine struct {
	text    string
	raw     bool
	command bool
	from    line
}

type phaseTally struct {
	since       time.Time
	units, done int
	overlapped  bool
}

type unitBlock struct {
	opened    *streamv1.RunEvent
	body      []blockLine
	resources resourceTally
}

func NewGroupedSink(w io.Writer, present Presentation) *GroupedSink {
	ticker := time.NewTicker(time.Second)
	s := newGroupedSink(w, present, ticker.C)
	s.stopTicks = ticker.Stop
	return s
}

func newGroupedSink(w io.Writer, present Presentation, ticks <-chan time.Time) *GroupedSink {
	stop := make(chan struct{})
	s := &GroupedSink{
		w:         w,
		present:   present,
		units:     make(map[string]*unitBlock),
		owners:    make(map[string]string),
		tallies:   make(map[progressv1.Phase]*phaseTally),
		stopBeats: sync.OnceFunc(func() { close(stop) }),
		stopTicks: func() {},
		beating:   make(chan struct{}),
	}
	go s.beatOn(ticks, stop)
	return s
}

func (s *GroupedSink) beatOn(ticks <-chan time.Time, stop <-chan struct{}) {
	defer close(s.beating)
	for {
		select {
		case <-stop:
			return
		case at := <-ticks:
			s.beat(at)
		}
	}
}

func (s *GroupedSink) beat(at time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.held || len(s.started) == 0 || at.Before(s.beatAt) {
		return
	}
	var phases []progressv1.Phase
	running := map[progressv1.Phase][]string{}
	for _, span := range s.started {
		opened := s.units[span].opened
		phase := opened.GetPhase()
		if running[phase] == nil {
			phases = append(phases, phase)
		}
		running[phase] = append(running[phase], cmp.Or(opened.GetSubject(), opened.GetMessage()))
	}
	for _, phase := range phases {
		tally := s.tallies[phase]
		message := fmt.Sprintf("Still %s %s — %d/%d done, %s elapsed",
			phaseGerunds[phase], strings.Join(running[phase], ", "), tally.done, tally.units, formatDuration(at.Sub(tally.since)))
		s.print(blockLine{text: line{level: progressv1.Level_LEVEL_INFO, phase: phase, message: message, quiet: true}.render(s.present)})
	}
	s.beatAt = at.Add(heartbeatEvery)
}

func (s *GroupedSink) Receive(ev *streamv1.RunEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.silenceBroke = false
	defer s.heard(ev)
	s.changeStarted = s.changeStarted || events.IsChanging(ev.GetPhase())
	span := stageKey(ev.GetSpanId())
	switch {
	case ev.GetStarted() != nil:
		s.open(span, ev)
	case ev.GetEnded() != nil:
		s.end(span, ev)
	case ev.GetWaiting() != nil:
		s.held = true
		s.wait(ev.GetWaiting())
	case ev.GetResumed() != nil:
		s.held = false
		s.silenceBroke = true
		s.resume(ev)
	case ev.GetIdentity() != nil:
		s.tier, s.origin = ev.GetIdentity().GetTier(), ev.GetIdentity().GetOrigin()
		s.gate(identityLines(s.present, ev.GetIdentity()))
	case ev.GetPlan() != nil:
		s.gate(planLines(s.present, ev.GetPlan()))
	case ev.GetDnsManualRecords() != nil:
		s.dnsRecords(ev)
	case ev.GetOutcome() != nil:
		s.promotion = ev.GetOutcome().GetPromotionId()
	case ev.GetResult() != nil:
		s.conclude(ev)
	case ev.GetLevel() == progressv1.Level_LEVEL_DEBUG && !s.present.Verbose:
	case ev.GetOutput() != nil:
		s.within(span, blockLine{text: ev.GetMessage(), raw: true}, blockLine{text: ev.GetMessage(), raw: true})
	case ev.GetBody() == nil && ev.GetMessage() != "":
		s.within(span, s.detail(ev), s.alone(ev))
	}
}

func (s *GroupedSink) heard(ev *streamv1.RunEvent) {
	if s.silenceBroke || s.beatAt.IsZero() {
		s.beatAt = ev.GetTime().AsTime().Add(heartbeatAfter)
	}
}

func (s *GroupedSink) within(span string, inUnit, alone blockLine) {
	if unit := s.units[s.owners[span]]; unit != nil {
		unit.body = append(unit.body, inUnit)
		return
	}
	s.print(alone)
}

func (s *GroupedSink) alone(ev *streamv1.RunEvent) blockLine {
	return blockLine{text: lineOf(ev).render(s.present), from: lineOf(ev)}
}

func (s *GroupedSink) detail(ev *streamv1.RunEvent) blockLine {
	text := ev.GetMessage()
	if ev.GetLevel() == progressv1.Level_LEVEL_INFO {
		text = muted(s.present, text)
	}
	text = strings.ReplaceAll(text, "\n", "\n"+continuationIndent)
	if ev.GetLevel() != progressv1.Level_LEVEL_INFO {
		text = levelLabels[ev.GetLevel()].render(s.present) + " " + text
	}
	return blockLine{text: continuationIndent + text, from: lineOf(ev)}
}

func (s *GroupedSink) open(span string, ev *streamv1.RunEvent) {
	parent := stageKey(ev.GetStarted().GetParentSpanId())
	switch {
	case isBareScope(ev):
	case s.owners[parent] != "":
		s.owners[span] = s.owners[parent]
	default:
		s.units[span] = &unitBlock{opened: ev}
		tally := s.tally(ev)
		tally.overlapped = tally.overlapped || tally.units > tally.done
		tally.units++
		s.started = append(s.started, span)
		s.owners[span] = span
	}
}

func (s *GroupedSink) end(span string, ev *streamv1.RunEvent) {
	unit := s.units[span]
	if unit == nil {
		s.endResource(span, ev)
		return
	}
	s.forget(span)
	tally := s.tally(unit.opened)
	tally.done++
	ended := ev.GetEnded()
	took := formatDuration(endedDuration(ev, ended))
	failed := ended.GetStatus() == progressv1.SpanStatus_SPAN_STATUS_ERROR
	partial := !failed && ev.GetLevel() >= progressv1.Level_LEVEL_WARN
	head := line{
		level:   ev.GetLevel(),
		ends:    ended.GetStatus(),
		message: pastTense(unit.opened.GetMessage()),
		timing:  " in " + took + tally.finished(),
		outcome: unit.resources.summary(),
	}
	if partial && ev.GetMessage() != "" {
		head.message = ev.GetMessage()
	}
	if failed {
		head.message, head.timing = unit.opened.GetMessage()+" failed", " after "+took+tally.finished()
		if reason := ev.GetMessage(); reason != "" {
			head.outcome += ": " + reason
		}
	}
	if partial {
		head.ends = progressv1.SpanStatus_SPAN_STATUS_UNSPECIFIED
	}
	header := unit.header(head, s.present)
	if s.present.GitHubActions && !failed && !partial && len(unit.body) > 0 {
		title := header.from.render(Presentation{})
		header.text, header.command = "::group::"+workflowData.Replace(title), true
		s.print(header)
		s.print(unit.body...)
		s.print(blockLine{text: "::endgroup::", command: true})
		return
	}
	s.print(header)
	if failed || !s.failed {
		s.print(unit.body...)
	} else {
		s.print(slices.DeleteFunc(slices.Clone(unit.body), func(l blockLine) bool {
			return l.raw || l.from.level < progressv1.Level_LEVEL_WARN
		})...)
	}
	s.failed = s.failed || failed
}

func (s *GroupedSink) endResource(span string, ev *streamv1.RunEvent) {
	unit := s.units[s.owners[span]]
	change, ok := resourceChangeOf(ev)
	if unit == nil || !ok {
		return
	}
	unit.resources.count(change)
	unit.body = append(unit.body, blockLine{text: continuationIndent + change.render(s.present)})
}

func (s *GroupedSink) tally(opened *streamv1.RunEvent) *phaseTally {
	tally := s.tallies[opened.GetPhase()]
	if tally == nil {
		tally = &phaseTally{since: opened.GetTime().AsTime()}
		s.tallies[opened.GetPhase()] = tally
	}
	return tally
}

func (t *phaseTally) finished() string {
	if !t.overlapped {
		return ""
	}
	return fmt.Sprintf(" (%d/%d)", t.done, t.units)
}

func (s *GroupedSink) dnsRecords(ev *streamv1.RunEvent) {
	records := ev.GetDnsManualRecords()
	if len(records.GetRecords()) == 0 {
		return
	}
	head := lineOf(ev)
	head.message = dnsHeadline(records.GetHeadline(), records.GetRecords())
	s.gap()
	s.print(blockLine{text: head.render(s.present), from: head})
	s.gate(dnsTable(records, s.present.Width))
}

func (s *GroupedSink) wait(waiting *streamv1.WaitingEvent) {
	if waiting.GetMissing() == nil && waiting.GetUrl() == "" {
		return
	}
	s.gated = true
	lines := MissingVariablesLines(waiting.GetMissing(), s.present)
	s.gate(append(lines,
		"",
		blockIndent+"Fill them in at:",
		"",
		blockIndent+blockIndent+waiting.GetUrl(),
		"",
		blockIndent+"Waiting for the page — press Ctrl-C to abort. Nothing has been provisioned.",
	))
}

func (s *GroupedSink) resume(ev *streamv1.RunEvent) {
	if !s.gated {
		return
	}
	s.gated = false
	resumed := lineOf(ev)
	resumed.message = "Resumed — " + ev.GetResumed().GetReason()
	s.print(blockLine{text: resumed.render(s.present), from: resumed})
}

func (s *GroupedSink) gate(lines []string) {
	if len(lines) == 0 {
		return
	}
	s.gap()
	for _, text := range lines {
		s.print(blockLine{text: text})
	}
	s.gap()
}

func (s *GroupedSink) gap() {
	if s.wrote && !s.blank {
		fmt.Fprintln(s.w)
		s.blank = true
	}
}

func (s *GroupedSink) print(lines ...blockLine) {
	for _, l := range lines {
		s.silenceBroke = true
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
		text := l.text
		if s.present.GitHubActions && !l.command {
			text = workflowCommandStart.ReplaceAllString(text, "${1}${2}"+commandBreak+"${3}")
		}
		fmt.Fprintln(s.w, text)
		s.annotate(l.from.level, l.from.annotation())
	}
}

func (s *GroupedSink) annotate(level progressv1.Level, text string) {
	if command, ok := annotationCommands[level]; ok && s.present.GitHubActions {
		fmt.Fprintf(s.w, "::%s::%s\n", command, workflowData.Replace(text))
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

func (u *unitBlock) header(ended line, present Presentation) blockLine {
	ended.phase, ended.subject, ended.heads = u.opened.GetPhase(), u.opened.GetSubject(), true
	return blockLine{text: ended.render(present), from: ended}
}

func (s *GroupedSink) Close() error {
	s.stopTicks()
	s.stopBeats()
	<-s.beating
	s.mu.Lock()
	defer s.mu.Unlock()
	s.unfinished()
	if s.verbatim {
		s.gap()
		s.verbatim = false
	}
	return nil
}

func (s *GroupedSink) unfinished() {
	for _, span := range slices.Clone(s.started) {
		unit := s.units[span]
		s.forget(span)
		s.print(unit.header(line{level: progressv1.Level_LEVEL_WARN, message: unit.opened.GetMessage() + " did not finish"}, s.present))
		s.print(unit.body...)
	}
}

func stageKey(id []byte) string {
	if len(id) == 0 {
		return ""
	}
	return hex.EncodeToString(id)
}

func endedDuration(ev *streamv1.RunEvent, ended *progressv1.Ended) time.Duration {
	if ev.GetTime() == nil {
		return 0
	}
	return elapsed(ended.GetStartTimeUnixNano(), ev.GetTime().AsTime().UnixNano())
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
