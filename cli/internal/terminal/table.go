package terminal

import (
	"maps"
	"slices"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/ocelhq/ocel/pkg/provider"
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
	split := make([][]string, len(cells))
	height := 1
	for i, cell := range cells {
		split[i] = strings.Split(cell, "\n")
		height = max(height, len(split[i]))
	}
	for line := range height {
		row := make([]string, len(cells))
		for i, parts := range split {
			if line < len(parts) {
				row[i] = parts[line]
			}
		}
		t.lines = append(t.lines, tableLine{cells: row})
	}
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

func AbbreviateRelease(release string) string {
	if parsed, err := provider.ParseRelease(release); err == nil {
		return AbbreviateID(parsed.BuildID())
	}
	return AbbreviateID(release)
}

func FormatReleases(p Palette, releaseByApp map[string]string) string {
	if len(releaseByApp) == 0 {
		return NoValue
	}
	lines := make([]string, 0, len(releaseByApp))
	for _, app := range slices.Sorted(maps.Keys(releaseByApp)) {
		lines = append(lines, app+" "+p.Muted(AbbreviateRelease(releaseByApp[app])))
	}
	return strings.Join(lines, "\n")
}

func AbbreviateID(id string) string {
	if len(id) <= shortIDLen {
		return id
	}
	return id[:shortIDLen]
}

func tableWidth(widths []int) int {
	if len(widths) == 0 {
		return 0
	}
	return sum(widths) + len(tableGutter)*(len(widths)-1)
}
