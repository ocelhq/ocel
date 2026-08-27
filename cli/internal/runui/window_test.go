package runui

import (
	"slices"
	"testing"
)

func running(output bool) windowUnit { return windowUnit{tier: tierRunning, output: output} }
func failedUnit() windowUnit         { return windowUnit{tier: tierFailed} }
func pendingUnit() windowUnit        { return windowUnit{tier: tierPending} }
func doneUnit() windowUnit           { return windowUnit{tier: tierDone} }

func rowUnits(f windowFrame) []int {
	out := make([]int, 0, len(f.rows))
	for _, row := range f.rows {
		out = append(out, row.unit)
	}
	return out
}

func TestTheWindowRanksFailedOverRunningOverPendingOverDone(t *testing.T) {
	t.Parallel()
	f := planWindow([]windowUnit{doneUnit(), pendingUnit(), running(false), failedUnit()}, 10)
	if got := rowUnits(f); !slices.Equal(got, []int{3, 2, 1, 0}) {
		t.Errorf("row order = %v, want failed, running, pending, done", got)
	}
	if f.more != 0 {
		t.Errorf("more = %d, want every unit visible in a 10-line window", f.more)
	}
}

func outputFlags(f windowFrame) []bool {
	out := make([]bool, 0, len(f.rows))
	for _, row := range f.rows {
		out = append(out, row.output)
	}
	return out
}

func TestRunningUnitsDropTheirOutputLineBeforeAnyUnitCollapses(t *testing.T) {
	t.Parallel()
	f := planWindow([]windowUnit{running(true), running(true), running(true)}, 5)
	if len(f.rows) != 3 || f.more != 0 {
		t.Fatalf("rows = %v, more = %d, want all three running units still on screen", rowUnits(f), f.more)
	}
	for i, output := range outputFlags(f) {
		if output {
			t.Errorf("row %d kept its output line, want every running unit degraded to one line", i)
		}
	}
}

func TestRunningUnitsDegradeUniformlyNeverMixedHeights(t *testing.T) {
	t.Parallel()
	f := planWindow([]windowUnit{running(true), running(false), running(true)}, 4)
	if len(f.rows) != 3 {
		t.Fatalf("rows = %v, want all three running units still on screen", rowUnits(f))
	}
	for i, output := range outputFlags(f) {
		if output {
			t.Errorf("row %d kept its output line, want the whole running tier at one height", i)
		}
	}
}

func TestATwentyFirstUnitDegradesIntoTheOverflowLineAndComesBackWhenSpaceFrees(t *testing.T) {
	t.Parallel()
	crowd := make([]windowUnit, 21)
	for i := range crowd {
		crowd[i] = running(true)
	}

	f := planWindow(crowd, 10)
	if len(f.rows) != 9 || f.more != 12 {
		t.Fatalf("rows = %d, more = %d, want 9 rows and the rest in the overflow line", len(f.rows), f.more)
	}
	if got := overflowLine(f); got != "+12 more: 12 running" {
		t.Errorf("overflow line = %q, want the hidden units counted by state", got)
	}

	crowd[20] = failedUnit()
	f = planWindow(crowd, 10)
	if got := rowUnits(f); len(got) == 0 || got[0] != 20 {
		t.Errorf("rows = %v, want the unit that failed back on screen at the top of the frame", got)
	}
}

func TestOnlyRunningUnitsCarryAnOutputLine(t *testing.T) {
	t.Parallel()
	terminal := windowUnit{tier: tierFailed, output: true}
	f := planWindow([]windowUnit{terminal, running(true)}, 20)
	if got := outputFlags(f); len(got) != 2 || got[0] {
		t.Errorf("output lines = %v, want the failed unit pinned as a single row", got)
	}
}

func TestFailedUnitsStayPinnedAheadOfEveryRunningUnit(t *testing.T) {
	t.Parallel()
	spine := []windowUnit{running(true), running(true), failedUnit(), running(true), failedUnit()}

	f := planWindow(spine, 3)
	if got := rowUnits(f); !slices.Equal(got, []int{2, 4}) {
		t.Errorf("rows = %v, want both failed units pinned in spine order and the running tier collapsed", got)
	}
	if got := overflowLine(f); got != "+3 more: 3 running" {
		t.Errorf("overflow line = %q, want the collapsed running units counted", got)
	}
}

func TestTheOverflowLineCountsFailedFirst(t *testing.T) {
	t.Parallel()
	f := planWindow([]windowUnit{running(false), pendingUnit(), pendingUnit(), pendingUnit(), pendingUnit(), doneUnit(), doneUnit()}, 2)
	if got := overflowLine(f); got != "+6 more: 4 waiting · 2 done" {
		t.Errorf("overflow line = %q", got)
	}

	crowded := make([]windowUnit, 12)
	for i := range crowded {
		crowded[i] = failedUnit()
	}
	f = planWindow(crowded, 1)
	if len(f.rows) != 0 {
		t.Errorf("rows = %v, want the window to give its only line to the overflow summary", rowUnits(f))
	}
	if got := overflowLine(f); got != "+12 more: 12 failed" {
		t.Errorf("overflow line = %q, want the guarantee to degrade loudly", got)
	}
}

func TestTheWindowKeepsSpineOrderWithinATier(t *testing.T) {
	t.Parallel()
	f := planWindow([]windowUnit{running(false), doneUnit(), running(false), running(false)}, 10)
	if got := rowUnits(f); !slices.Equal(got, []int{0, 2, 3, 1}) {
		t.Errorf("row order = %v, want the running units in spine order ahead of the done one", got)
	}
}

func TestPendingUnitsFallBelowTheFoldBeforeRunningUnitsLoseTheirOutput(t *testing.T) {
	t.Parallel()
	spine := []windowUnit{running(true), running(true)}
	for len(spine) < 16 {
		spine = append(spine, pendingUnit())
	}

	f := planWindow(spine, 17)
	if len(f.rows) < 2 || !f.rows[0].output || !f.rows[1].output {
		t.Fatalf("output lines = %v, want both running units keeping theirs — the running tier needs 4 of 17 lines", outputFlags(f))
	}
	if got := overflowLine(f); got != "+2 more: 2 waiting" {
		t.Errorf("overflow line = %q, want the pending units that do not fit counted below the fold", got)
	}
}

func TestALoneRunningUnitKeepsItsOutputHoweverManyUnitsWait(t *testing.T) {
	t.Parallel()
	spine := []windowUnit{running(true)}
	for len(spine) < 31 {
		spine = append(spine, pendingUnit())
	}

	f := planWindow(spine, 24)
	if len(f.rows) == 0 || !f.rows[0].output {
		t.Fatalf("output lines = %v, want the single running unit to keep its output line", outputFlags(f))
	}
}
