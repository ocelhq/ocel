package terminal

import (
	"cmp"
	"fmt"
	"strings"
	"time"

	"github.com/ocelhq/ocel/cli/internal/run"
	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
)

const (
	heartbeatAfter = 30 * time.Second
	heartbeatEvery = 60 * time.Second
)

func (s *Transcript) runHeartbeats(ticks <-chan time.Time, stop <-chan struct{}) {
	defer close(s.heartbeatsStopped)
	for {
		select {
		case <-stop:
			return
		case at := <-ticks:
			s.printHeartbeat(at)
		}
	}
}

func (s *Transcript) printHeartbeat(at time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.held || len(s.spans.running) == 0 || at.Before(s.nextHeartbeat) {
		return
	}
	var running []progressv1.Phase
	names := map[progressv1.Phase][]string{}
	for _, span := range s.spans.running {
		opened := s.spans.opened[span]
		phase := opened.GetPhase()
		if names[phase] == nil {
			running = append(running, phase)
		}
		names[phase] = append(names[phase], cmp.Or(opened.GetSubject(), opened.GetMessage()))
	}
	for _, phase := range running {
		tally := s.spans.tallies[phase]
		described, _ := run.DescribePhase(phase)
		doing := cmp.Or(described.Gerund, "running:")
		message := fmt.Sprintf("Still %s %s — %d/%d done, %s elapsed",
			doing, strings.Join(names[phase], ", "), tally.done, tally.units, formatDuration(at.Sub(tally.since)))
		s.print(blockLine{text: line{level: progressv1.Level_LEVEL_INFO, phase: phase, message: message, dim: true}.render(s.present)})
	}
	s.nextHeartbeat = at.Add(heartbeatEvery)
}

func (s *Transcript) postponeHeartbeat(ev *streamv1.RunEvent) {
	if s.shownForEvent || s.nextHeartbeat.IsZero() {
		s.nextHeartbeat = ev.GetTime().AsTime().Add(heartbeatAfter)
	}
}
