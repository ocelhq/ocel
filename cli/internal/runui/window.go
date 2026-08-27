package runui

import (
	"fmt"
	"strings"
)

type unitTier int

const (
	tierFailed unitTier = iota
	tierRunning
	tierPending
	tierDone
)

var tiers = []unitTier{tierFailed, tierRunning, tierPending, tierDone}

type windowUnit struct {
	tier   unitTier
	output bool
}

type windowRow struct {
	unit   int
	output bool
}

type tierCount struct {
	tier  unitTier
	count int
}

type windowFrame struct {
	rows   []windowRow
	hidden []tierCount
	more   int
}

func planWindow(us []windowUnit, height int) windowFrame {
	order := priorityOrder(us)
	if frame, ok := fit(us, order, height, true); ok {
		return frame
	}
	if frame, ok := fit(us, order, height, false); ok {
		return frame
	}
	return collapse(us, order, height)
}

func collapse(us []windowUnit, order []int, height int) windowFrame {
	capacity := height - 1
	if capacity < 0 {
		capacity = 0
	}
	if capacity > len(order) {
		capacity = len(order)
	}
	rows := make([]windowRow, 0, capacity)
	for _, i := range order[:capacity] {
		rows = append(rows, windowRow{unit: i})
	}
	return windowFrame{rows: rows, hidden: countTiers(us, order[capacity:]), more: len(order) - capacity}
}

func countTiers(us []windowUnit, hidden []int) []tierCount {
	var out []tierCount
	for _, tier := range tiers {
		count := 0
		for _, i := range hidden {
			if us[i].tier == tier {
				count++
			}
		}
		if count > 0 {
			out = append(out, tierCount{tier: tier, count: count})
		}
	}
	return out
}

func overflowLine(f windowFrame) string {
	if f.more == 0 {
		return ""
	}
	parts := make([]string, 0, len(f.hidden))
	for _, c := range f.hidden {
		parts = append(parts, fmt.Sprintf("%d %s", c.count, tierWord(c.tier)))
	}
	return fmt.Sprintf("+%d more: %s", f.more, strings.Join(parts, " · "))
}

func tierWord(t unitTier) string {
	switch t {
	case tierFailed:
		return "failed"
	case tierRunning:
		return "running"
	case tierPending:
		return "waiting"
	default:
		return "done"
	}
}

func fit(us []windowUnit, order []int, height int, output bool) (windowFrame, bool) {
	rows := make([]windowRow, 0, len(order))
	lines := 0
	for _, i := range order {
		row := windowRow{unit: i, output: output && us[i].output && us[i].tier == tierRunning}
		rows = append(rows, row)
		lines++
		if row.output {
			lines++
		}
	}
	return windowFrame{rows: rows}, lines <= height
}

func priorityOrder(us []windowUnit) []int {
	order := make([]int, 0, len(us))
	for _, tier := range tiers {
		for i, u := range us {
			if u.tier == tier {
				order = append(order, i)
			}
		}
	}
	return order
}
