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

func rowTitles(rows []displayRow) []string {
	titles := make([]string, 0, len(rows))
	for _, row := range rows {
		titles = append(titles, row.n.title)
	}
	return titles
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
		if p.hasActiveAncestor(key) {
			t.Errorf("hasActiveAncestor(%q) = true, want a re-rooted stage to own its commit", p.nodes[key].title)
		}
		if got := p.subtreeRows(key); len(got) != 1 {
			t.Errorf("subtreeRows(%q) = %v, want the stage alone", p.nodes[key].title, rowTitles(got))
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

func TestSubtreeOrderFollowsDeclaration(t *testing.T) {
	t.Parallel()

	parent := appStage(1)
	first, second, third := appStage(2), appStage(3), appStage(4)
	p := newStagePlan()
	p.apply(&progressv1.StagePlanEvent{Stages: []*progressv1.Stage{
		{Id: parent, Title: "parent"},
		{Id: first, ParentId: parent, Title: "first"},
		{Id: second, ParentId: parent, Title: "second"},
		{Id: third, ParentId: parent, Title: "third"},
	}})

	activate(p, parent, third, first, second)

	live := p.units()
	if len(live) != 1 || live[0].root.title != "parent" {
		t.Fatalf("units() = %v, want the one unit the stages hang off", unitTitles(live))
	}
	if live[0].output == nil || live[0].output.title != "first" {
		t.Fatalf("output line = %+v, want the first child in declaration order, not the first activated", live[0].output)
	}
}
