package envgate

import (
	"cmp"
	"slices"
)

type heldCells map[Cell]int64

func (h heldCells) has(cell Cell) bool {
	_, ok := h[cell]
	return ok
}

func cellsOf(held heldCells, key string) []Cell {
	var out []Cell
	for cell := range held {
		if cell.Key == key {
			out = append(out, cell)
		}
	}
	slices.SortFunc(out, func(a, b Cell) int { return cmp.Compare(a.Folder, b.Folder) })
	return out
}

func (g *Gate) classWideCells() heldCells {
	cells := make(heldCells, len(g.cells))
	for _, row := range g.cells {
		cells[row.Cell] = row.Version
	}
	return cells
}

func (g *Gate) resolvedCells() heldCells {
	cells := g.classWideCells()
	if g.scope.Environment == "" {
		return cells
	}
	for cell := range g.overrides {
		if version, ok := g.ownOverride(cell); ok {
			cells[cell] = version
		}
	}
	return cells
}

func (g *Gate) ownOverride(cell Cell) (int64, bool) {
	if g.scope.Environment == "" {
		return 0, false
	}
	for _, override := range g.overrides[cell] {
		if override.Environment == g.scope.Environment {
			return override.Version, true
		}
	}
	return 0, false
}

func (g *Gate) resolvedEnvironment(cell Cell) string {
	if _, ok := g.ownOverride(cell); ok {
		return g.scope.Environment
	}
	return ""
}
