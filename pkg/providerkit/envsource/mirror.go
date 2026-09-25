package envsource

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"

	"github.com/ocelhq/ocel/pkg/providerkit/values"
)

type Report struct {
	Source    string
	Present   []values.Cell
	Written   []values.Cell
	Removed   []values.Cell
	Unchanged int
	Refused   map[values.Cell]string
}

func Mirror(ctx context.Context, store values.Store, scope values.Scope, source Source, folders []string, keep []values.Cell) (Report, error) {
	resolved, err := source.Resolve(ctx, folders)
	if err != nil {
		return Report{}, err
	}
	return Apply(ctx, store, scope, source.ID(), resolved, folders, keep)
}

func Apply(ctx context.Context, store values.Store, scope values.Scope, id string, resolved map[values.Cell]Resolved, folders []string, keep []values.Cell) (Report, error) {
	report := Report{Source: id}
	listed, err := store.List(ctx, scope)
	if err != nil {
		return Report{}, err
	}
	held := map[values.Cell]values.Metadata{}
	for _, metadata := range listed {
		if metadata.Coordinate.Environment == "" {
			held[metadata.Coordinate.Cell] = metadata
		}
	}

	for _, at := range sortedCells(resolved) {
		if !slices.Contains(folders, at.Folder) || slices.Contains(keep, at) {
			continue
		}
		report.Present = append(report.Present, at)
		read := resolved[at]
		current, found := held[at]
		if found && current.Provenance.EnvSource == id && !supersedes(read.Version, current.Provenance.Version) {
			report.Unchanged++
			continue
		}
		var expected int64
		if found {
			expected = current.Version
		}
		_, err := store.Mirror(ctx, scope, values.Coordinate{Cell: at}, string(read.Value), values.Provenance{EnvSource: id, Version: read.Version}, expected)
		switch {
		case err == nil:
			report.Written = append(report.Written, at)
		case errors.Is(err, values.ErrStaleVersion):
		case errors.Is(err, values.ErrTooLarge), errors.Is(err, values.ErrIsReference):
			report.refuse(at, err)
		default:
			return Report{}, err
		}
	}

	for _, metadata := range listed {
		at := metadata.Coordinate.Cell
		if metadata.Coordinate.Environment != "" || metadata.Provenance.EnvSource != id || !slices.Contains(folders, at.Folder) || slices.Contains(keep, at) {
			continue
		}
		if _, present := resolved[at]; present {
			continue
		}
		expected := metadata.Version
		removed, err := store.Delete(ctx, scope, metadata.Coordinate, &expected)
		if errors.Is(err, values.ErrStaleVersion) {
			continue
		}
		if err != nil {
			return Report{}, err
		}
		if removed {
			report.Removed = append(report.Removed, at)
		}
	}
	return report, nil
}

func (r *Report) refuse(at values.Cell, err error) {
	if r.Refused == nil {
		r.Refused = map[values.Cell]string{}
	}
	r.Refused[at] = err.Error()
}

func sortedCells[V any](held map[values.Cell]V) []values.Cell {
	out := make([]values.Cell, 0, len(held))
	for at := range held {
		out = append(out, at)
	}
	slices.SortFunc(out, compareCells)
	return out
}

func compareCells(a, b values.Cell) int {
	return cmp.Or(cmp.Compare(a.Folder, b.Folder), cmp.Compare(a.Key, b.Key))
}

func supersedes(read, held string) bool {
	if read == held {
		return false
	}
	readID, readN, readOrdered := ordinal(read)
	heldID, heldN, heldOrdered := ordinal(held)
	if readOrdered && heldOrdered && readID == heldID {
		return readN > heldN
	}
	return true
}

func ordinal(version string) (string, int64, bool) {
	id, n, split := strings.Cut(version, "@")
	if !split {
		return "", 0, false
	}
	parsed, err := strconv.ParseInt(n, 10, 64)
	return id, parsed, err == nil
}
