package runui

import (
	"testing"
	"time"

	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
)

func activate(p *stagePlan, ids ...[]byte) {
	for _, id := range ids {
		key := stageKey(id)
		p.progress(key, "working", 0, nil)
		p.ensureActive(key)
	}
}

func TestCyclicDeclarationsStayVisible(t *testing.T) {
	t.Parallel()

	selfy, left, right := appStage(1), appStage(2), appStage(3)
	p := newStagePlan()
	p.apply(&progressv1.StagePlanEvent{Stages: []*progressv1.Stage{
		{Id: selfy, ParentId: selfy, Title: "selfy"},
		{Id: left, ParentId: right, Title: "left"},
		{Id: right, ParentId: left, Title: "right"},
	}})

	activate(p, selfy, left, right)

	live := p.units()
	if len(live) != 3 {
		t.Fatalf("units() = %v, want one unit per re-rooted cyclic stage", unitTitles(live))
	}
	for _, u := range live {
		if u.output != nil {
			t.Errorf("unit %q carries an output line from %q, want a re-rooted stage to own its own row", u.root.title, u.output.title)
		}
	}

	for _, id := range [][]byte{selfy, left, right} {
		key := stageKey(id)
		p.foldSubtree(key)
		if len(p.activeOrder) != 3 {
			t.Fatalf("activeOrder = %v, want ending %q to take no re-rooted sibling down with it", p.activeOrder, p.nodes[key].title)
		}
	}
}

func TestRestartRevivesACommittedStage(t *testing.T) {
	t.Parallel()

	now := time.Unix(0, 0)
	p := newStagePlan()
	p.useClock(func() time.Time { return now })

	key := stageKey(buildStageID)
	p.progress(key, "Building project", 0, nil)
	p.ensureActive(key)

	n := p.nodes[key]
	n.state = stageDone
	n.doneDur = 90 * time.Second
	p.removeActive(key)

	now = now.Add(2 * time.Minute)
	p.restart(key)

	if n.state != stageActive {
		t.Errorf("state = %v, want the restarted build stage active again", n.state)
	}
	if !p.isActive(key) {
		t.Error("isActive() = false, want the restarted build stage back in the live region")
	}
	if !n.started.Equal(now) {
		t.Errorf("started = %v, want the clock at restart %v", n.started, now)
	}
	if n.doneDur != 0 {
		t.Errorf("doneDur = %v, want the discarded attempt's duration cleared", n.doneDur)
	}
}

func unitTitles(live []liveUnit) []string {
	titles := make([]string, 0, len(live))
	for _, u := range live {
		titles = append(titles, u.root.title)
	}
	return titles
}
