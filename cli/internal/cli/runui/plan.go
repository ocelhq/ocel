package runui

import (
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/fatih/color"
)

const (
	planIndent = "  "
	rowIndent  = "    "
	kindGutter = "  "
)

type PlanPrinter struct {
	out   io.Writer
	color bool
}

func NewPlanPrinter(out io.Writer, colorEnabled bool) *PlanPrinter {
	return &PlanPrinter{out: out, color: colorEnabled}
}

func (p *PlanPrinter) Print(plan *Plan) {
	header := plan.Subject
	if plan.EdgeKind != "" {
		header += fmt.Sprintf(", fronted by the %s edge", plan.EdgeKind)
	}
	fmt.Fprintf(p.out, "%s:\n", p.style(color.Bold).Sprint(header))

	for _, group := range plan.Groups {
		rows := moving(group.Rows)
		if len(rows) == 0 {
			continue
		}
		fmt.Fprintln(p.out)
		fmt.Fprintln(p.out, planIndent+p.groupHead(group))
		for _, line := range p.rowLines(rows) {
			fmt.Fprintln(p.out, line)
		}
	}

	fmt.Fprintf(p.out, "\n%s\n", p.tally(plan))
}

func (p *PlanPrinter) groupHead(group Group) string {
	var b strings.Builder
	if group.Kind != "" {
		b.WriteString(p.faint(group.Kind) + " ")
	}
	b.WriteString(p.style(color.Bold).Sprint(group.Name))
	if group.Feature != "" {
		b.WriteString(kindGutter + p.faint("["+group.Feature+"]"))
	}
	return b.String()
}

func (p *PlanPrinter) rowLines(rows []Row) []string {
	width := 0
	for _, row := range rows {
		width = max(width, utf8.RuneCountInString(label(row)))
	}
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		text := label(row)
		pad := strings.Repeat(" ", width-utf8.RuneCountInString(text))
		var b strings.Builder
		fmt.Fprintf(&b, "%s%s %s", rowIndent, p.paint(row.Action), text)
		if row.Kind != "" {
			b.WriteString(pad + kindGutter + p.faint(row.Kind))
		}
		if row.Reason != "" {
			b.WriteString(p.faint(kindGutter + "— " + row.Reason))
		}
		if row.Slow {
			b.WriteString(p.faint(" (this one is slow)"))
		}
		out = append(out, b.String())
	}
	return out
}

func label(row Row) string {
	if row.Action == DisableThenDelete {
		return "disable, then delete " + row.Name
	}
	return row.Name
}

func moving(rows []Row) []Row {
	out := make([]Row, 0, len(rows))
	for _, row := range rows {
		if row.Action != Keep {
			out = append(out, row)
		}
	}
	return out
}

func (p *PlanPrinter) tally(plan *Plan) string {
	counts := map[Action]int{}
	for _, group := range plan.Groups {
		for _, row := range group.Rows {
			action := row.Action
			if action == DisableThenDelete {
				action = Delete
			}
			counts[action]++
		}
	}
	var parts []string
	for _, action := range []Action{Create, Update, Replace, Delete} {
		if n := counts[action]; n > 0 {
			parts = append(parts, fmt.Sprintf("%d to %s", n, verb(action)))
		}
	}
	if len(parts) == 0 {
		parts = append(parts, "Nothing to change")
	}
	line := strings.Join(parts, ", ") + "."
	if kept := counts[Keep]; kept > 0 {
		line += p.faint(fmt.Sprintf(" %d unchanged.", kept))
	}
	return line
}

func verb(action Action) string {
	switch action {
	case Create:
		return "create"
	case Update:
		return "update"
	case Replace:
		return "replace"
	default:
		return "delete"
	}
}

func Moving(plan *Plan) bool {
	for _, group := range plan.Groups {
		if len(moving(group.Rows)) > 0 {
			return true
		}
	}
	return false
}

var sigilAttrs = map[string][]color.Attribute{
	"+": {color.FgGreen},
	"~": {color.FgYellow},
	"±": {color.FgYellow},
	"–": {color.FgRed},
}

func sigil(action Action) string {
	switch action {
	case Create:
		return "+"
	case Update:
		return "~"
	case Replace:
		return "±"
	case Delete, DisableThenDelete:
		return "–"
	default:
		return " "
	}
}

func (p *PlanPrinter) paint(action Action) string {
	glyph := sigil(action)
	attrs, ok := sigilAttrs[glyph]
	if !ok {
		return glyph
	}
	return p.style(attrs...).Sprint(glyph)
}

func (p *PlanPrinter) faint(s string) string { return p.style(color.Faint).Sprint(s) }

func (p *PlanPrinter) style(attrs ...color.Attribute) *color.Color {
	c := color.New(attrs...)
	if p.color {
		c.EnableColor()
	} else {
		c.DisableColor()
	}
	return c
}
