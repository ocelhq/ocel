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
	rich := fill(us, order, height, true)
	if bare := fill(us, order, height, false); runningRows(us, bare) > runningRows(us, rich) {
		return bare
	}
	return rich
}

func fill(us []windowUnit, order []int, height int, withOutput bool) windowFrame {
	rows := make([]windowRow, 0, len(order))
	lines := 0
	for taken, i := range order {
		row := windowRow{unit: i, output: withOutput && us[i].output && us[i].tier == tierRunning}
		cost := 1
		if row.output {
			cost++
		}
		budget := height
		if taken < len(order)-1 {
			budget--
		}
		if lines+cost > budget {
			break
		}
		rows = append(rows, row)
		lines += cost
	}
	hidden := order[len(rows):]
	return windowFrame{rows: rows, hidden: countTiers(us, hidden), more: len(hidden)}
}

func runningRows(us []windowUnit, f windowFrame) int {
	n := 0
	for _, row := range f.rows {
		if us[row.unit].tier == tierRunning {
			n++
		}
	}
	return n
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
