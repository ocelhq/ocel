package runui

import (
	"fmt"
	"strings"
)

type unitTier int

const (
	tierFailed unitTier = iota
	tierRunning
)

var tiers = []unitTier{tierFailed, tierRunning}

type tierCount struct {
	tier  unitTier
	count int
}

type windowFrame struct {
	rows   []int
	hidden []tierCount
	more   int
}

func planWindow(us []unitTier, height int) windowFrame {
	order := priorityOrder(us)
	rows := make([]int, 0, len(order))
	for taken, i := range order {
		budget := height
		if taken < len(order)-1 {
			budget--
		}
		if len(rows)+1 > budget {
			break
		}
		rows = append(rows, i)
	}
	hidden := order[len(rows):]
	return windowFrame{rows: rows, hidden: countTiers(us, hidden), more: len(hidden)}
}

func countTiers(us []unitTier, hidden []int) []tierCount {
	var out []tierCount
	for _, tier := range tiers {
		count := 0
		for _, i := range hidden {
			if us[i] == tier {
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
	if t == tierFailed {
		return "failed"
	}
	return "running"
}

func priorityOrder(us []unitTier) []int {
	order := make([]int, 0, len(us))
	for _, tier := range tiers {
		for i, u := range us {
			if u == tier {
				order = append(order, i)
			}
		}
	}
	return order
}
