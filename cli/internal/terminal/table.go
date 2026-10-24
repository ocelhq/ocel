package terminal

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

const (
	NoValue     = "—"
	tableGutter = "  "
	ruleGlyph   = "─"
	shortIDLen  = 8
)

type Alignment int

const (
	AlignLeft Alignment = iota
	AlignRight
)

type Column struct {
	Head  string
	Align Alignment
}

type Table struct {
	columns []Column
	lines   []tableLine
}

type tableLine struct {
	cells []string
	span  string
	rule  bool
}

func NewTable(columns ...Column) *Table {
	return &Table{columns: columns}
}

func (t *Table) Row(cells ...string) {
	t.lines = append(t.lines, tableLine{cells: cells})
}

func (t *Table) Span(text string) {
	t.lines = append(t.lines, tableLine{span: text})
}

func (t *Table) Rule() {
	t.lines = append(t.lines, tableLine{rule: true})
}

func (t *Table) Render(p Palette, indent string) string {
	widths := t.widths()
	var b strings.Builder
	if heads := t.heads(); heads != nil {
		for i := range heads {
			heads[i] = p.Muted(heads[i])
		}
		b.WriteString(indent + t.line(heads, widths) + "\n")
	}
	for _, line := range t.lines {
		switch {
		case line.rule:
			b.WriteString(indent + p.Muted(strings.Repeat(ruleGlyph, tableWidth(widths))) + "\n")
		case line.cells == nil:
			b.WriteString(indent + line.span + "\n")
		default:
			cells := make([]string, len(t.columns))
			for i := range cells {
				if i < len(line.cells) {
					cells[i] = line.cells[i]
				}
				if cells[i] == NoValue {
					cells[i] = p.Muted(NoValue)
				}
			}
			b.WriteString(indent + t.line(cells, widths) + "\n")
		}
	}
	return b.String()
}

func (t *Table) heads() []string {
	heads := make([]string, len(t.columns))
	named := false
	for i, column := range t.columns {
		heads[i] = column.Head
		named = named || column.Head != ""
	}
	if !named {
		return nil
	}
	return heads
}

func (t *Table) widths() []int {
	widths := make([]int, len(t.columns))
	for i, column := range t.columns {
		widths[i] = ansi.StringWidth(column.Head)
	}
	for _, line := range t.lines {
		for i, cell := range line.cells {
			if i < len(widths) {
				widths[i] = max(widths[i], ansi.StringWidth(cell))
			}
		}
	}
	return widths
}

func (t *Table) line(cells []string, widths []int) string {
	var b strings.Builder
	for i, cell := range cells {
		if i > 0 {
			b.WriteString(tableGutter)
		}
		pad := strings.Repeat(" ", widths[i]-ansi.StringWidth(cell))
		if t.columns[i].Align == AlignRight {
			b.WriteString(pad + cell)
			continue
		}
		b.WriteString(cell + pad)
	}
	return strings.TrimRight(b.String(), " ")
}

func ShortID(id string) string {
	if len(id) <= shortIDLen {
		return id
	}
	return id[:shortIDLen]
}

func tableWidth(widths []int) int {
	return sum(widths) + len(tableGutter)*(len(widths)-1)
}
