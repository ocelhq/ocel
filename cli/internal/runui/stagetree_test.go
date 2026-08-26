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

	rows := p.displayRows()
	if len(rows) != 3 {
		t.Fatalf("displayRows() = %v, want one row per cyclic stage", rowTitles(rows))
	}
	for _, row := range rows {
		if row.depth != 0 {
			t.Errorf("row %q depth = %d, want a re-rooted stage at depth 0", row.n.title, row.depth)
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

	got := rowTitles(p.displayRows())
	want := []string{"parent", "first", "second", "third"}
	if len(got) != len(want) {
		t.Fatalf("displayRows() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("displayRows() = %v, want %v", got, want)
		}
	}

}
