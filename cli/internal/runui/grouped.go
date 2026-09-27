package runui

import (
	"cmp"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"

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
	w       io.Writer
	present Presentation

	mu           sync.Mutex
	units        map[string]*unitBlock
	started      []string
	owners       map[string]string
	verbatim     bool
	failed       bool
	tallies      map[progressv1.Phase]*phaseTally
	beatAt       time.Time
	silenceBroke bool
	held         bool
	wrote        bool
	tier         environmentv1.Tier
	promotion    string

	stopBeats func()
	stopTicks func()
	beating   chan struct{}
}

type phaseTally struct {
	since       time.Time
	units, done int
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
		s.print(blockLine{text: line{level: progressv1.Level_LEVEL_INFO, phase: phase, message: message}.render(s.present)})
	}
	s.beatAt = at.Add(heartbeatEvery)
}

func (s *GroupedSink) Receive(ev *streamv1.RunEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.silenceBroke = false
	defer s.heard(ev)
	span := stageKey(ev.GetSpanId())
	switch {
	case ev.GetStarted() != nil:
		s.open(span, ev)
	case ev.GetEnded() != nil:
		s.end(span, ev)
	case ev.GetWaiting() != nil:
		s.held = true
	case ev.GetResumed() != nil:
		s.held = false
		s.silenceBroke = true
	case ev.GetIdentity() != nil:
		s.tier = ev.GetIdentity().GetTier()
	case ev.GetOutcome() != nil:
		s.promotion = ev.GetOutcome().GetPromotionId()
	case ev.GetResult() != nil:
		s.conclude(ev)
	case ev.GetLevel() == progressv1.Level_LEVEL_DEBUG && !s.present.Verbose:
	case ev.GetOutput() != nil:
		s.within(span, blockLine{text: ev.GetMessage(), raw: true}, blockLine{text: ev.GetMessage(), raw: true})
	case ev.GetCounter() != nil:
		counted := proto.CloneOf(ev)
		counted.Message = progressLogLine(ev.GetMessage(), ev.GetCounter().GetCurrent(), ev.GetCounter().Total)
		s.within(span, s.detail(counted), s.alone(counted))
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
	text := strings.ReplaceAll(ev.GetMessage(), "\n", "\n"+continuationIndent)
	if ev.GetLevel() != progressv1.Level_LEVEL_INFO {
		text = levelLabels[ev.GetLevel()].render(s.present) + " " + text
	}
	return blockLine{text: continuationIndent + text, from: lineOf(ev)}
}

func (s *GroupedSink) open(span string, ev *streamv1.RunEvent) {
	parent := stageKey(ev.GetStarted().GetParentSpanId())
	switch {
	case parent == "" && ev.GetSubject() == "" && ev.GetMessage() == "":
	case s.owners[parent] != "":
		s.owners[span] = s.owners[parent]
	default:
		s.units[span] = &unitBlock{opened: ev}
		s.tally(ev).units++
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
	s.tally(unit.opened).done++
	ended := ev.GetEnded()
	took := formatDuration(endedDuration(ev, ended))
	failed := ended.GetStatus() == progressv1.SpanStatus_SPAN_STATUS_ERROR
	message := fmt.Sprintf("%s in %s%s", unit.opened.GetMessage(), took, unit.resources.summary())
	if failed {
		message = fmt.Sprintf("%s failed after %s%s", unit.opened.GetMessage(), took, unit.resources.summary())
		if reason := ev.GetMessage(); reason != "" {
			message += ": " + reason
		}
	}
	header := unit.header(ev.GetLevel(), ended.GetStatus(), message, s.present)
	if s.present.GitHubActions && !failed && len(unit.body) > 0 {
		header.text = "::group::" + workflowData.Replace(header.text)
		s.print(header)
		s.print(unit.body...)
		s.print(blockLine{text: "::endgroup::"})
		return
	}
	s.print(header)
	if failed || !s.failed {
		s.print(unit.body...)
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

func (s *GroupedSink) print(lines ...blockLine) {
	for _, l := range lines {
		s.silenceBroke = true
		s.wrote = true
		if l.raw != s.verbatim {
			fmt.Fprintln(s.w)
		}
		s.verbatim = l.raw
		if l.raw {
			fmt.Fprintln(s.w, verbatimIndent+l.text)
			continue
		}
		fmt.Fprintln(s.w, l.text)
		if command, ok := annotationCommands[l.from.level]; ok && s.present.GitHubActions {
			fmt.Fprintf(s.w, "::%s::%s\n", command, workflowData.Replace(l.from.annotation()))
		}
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
	return blockLine{text: head.render(present), from: head}
}

func (s *GroupedSink) Close() error {
	s.stopTicks()
	s.stopBeats()
	<-s.beating
	s.mu.Lock()
	defer s.mu.Unlock()
	s.unfinished()
	if s.verbatim {
		fmt.Fprintln(s.w)
		s.verbatim = false
	}
	return nil
}

func (s *GroupedSink) unfinished() {
	for _, span := range slices.Clone(s.started) {
		unit := s.units[span]
		s.forget(span)
		s.print(unit.header(progressv1.Level_LEVEL_WARN, progressv1.SpanStatus_SPAN_STATUS_UNSPECIFIED, unit.opened.GetMessage()+" did not finish", s.present))
		s.print(unit.body...)
	}
}
