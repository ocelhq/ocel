package runui

import (
	"slices"
	"testing"
)

func TestTheWindowRanksFailedAheadOfRunning(t *testing.T) {
	t.Parallel()
	f := planWindow([]unitTier{tierRunning, tierRunning, tierFailed}, 10)
	if !slices.Equal(f.rows, []int{2, 0, 1}) {
		t.Errorf("row order = %v, want the failed unit ahead of the running ones", f.rows)
	}
	if f.more != 0 {
		t.Errorf("more = %d, want every unit visible in a 10-line window", f.more)
	}
}

func TestEveryUnitCostsOneLineHoweverMuchItHasToSay(t *testing.T) {
	t.Parallel()
	f := planWindow([]unitTier{tierRunning, tierRunning, tierRunning}, 3)
	if !slices.Equal(f.rows, []int{0, 1, 2}) || f.more != 0 {
		t.Errorf("rows = %v, more = %d, want three running units in three lines", f.rows, f.more)
	}
}

func TestATwentyFirstUnitDegradesIntoTheOverflowLineAndComesBackWhenSpaceFrees(t *testing.T) {
	t.Parallel()
	crowd := make([]unitTier, 21)
	for i := range crowd {
		crowd[i] = tierRunning
	}

	f := planWindow(crowd, 10)
	if len(f.rows) != 9 || f.more != 12 {
		t.Fatalf("rows = %d, more = %d, want 9 rows and the rest in the overflow line", len(f.rows), f.more)
	}
	if got := overflowLine(f); got != "+12 more: 12 running" {
		t.Errorf("overflow line = %q, want the hidden units counted by state", got)
	}

	crowd[20] = tierFailed
	f = planWindow(crowd, 10)
	if len(f.rows) == 0 || f.rows[0] != 20 {
		t.Errorf("rows = %v, want the unit that failed back on screen at the top of the frame", f.rows)
	}
}

func TestFailedUnitsStayPinnedAheadOfEveryRunningUnit(t *testing.T) {
	t.Parallel()
	spine := []unitTier{tierRunning, tierRunning, tierFailed, tierRunning, tierFailed}

	f := planWindow(spine, 3)
	if !slices.Equal(f.rows, []int{2, 4}) {
		t.Errorf("rows = %v, want both failed units pinned in spine order and the running tier collapsed", f.rows)
	}
	if got := overflowLine(f); got != "+3 more: 3 running" {
		t.Errorf("overflow line = %q, want the collapsed running units counted", got)
	}
}

func TestTheOverflowLineCountsFailedFirst(t *testing.T) {
	t.Parallel()
	f := planWindow([]unitTier{tierRunning, tierRunning, tierFailed}, 2)
	if got := overflowLine(f); got != "+2 more: 2 running" {
		t.Errorf("overflow line = %q, want the failed unit on screen and the running ones counted below it", got)
	}

	crowded := make([]unitTier, 12)
	for i := range crowded {
		crowded[i] = tierFailed
	}
	f = planWindow(crowded, 1)
	if len(f.rows) != 0 {
		t.Errorf("rows = %v, want the window to give its only line to the overflow summary", f.rows)
	}
	if got := overflowLine(f); got != "+12 more: 12 failed" {
		t.Errorf("overflow line = %q, want the guarantee to degrade loudly", got)
	}
}

func TestTheWindowKeepsSpineOrderWithinATier(t *testing.T) {
	t.Parallel()
	f := planWindow([]unitTier{tierRunning, tierFailed, tierRunning, tierFailed}, 10)
	if !slices.Equal(f.rows, []int{1, 3, 0, 2}) {
		t.Errorf("row order = %v, want each tier held in spine order", f.rows)
	}
}
