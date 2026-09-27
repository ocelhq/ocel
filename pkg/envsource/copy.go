package envsource

import (
	"context"
	"errors"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/ocelhq/ocel/pkg/envvars"
)

type CopyResult struct {
	EnvSource string
	Present   []envvars.Cell
	Written   []envvars.Cell
	Unchanged []envvars.Cell
	Removed   []envvars.Cell
	Refused   map[envvars.Cell]string
}

func CopyValues(ctx context.Context, store envvars.Store, scope envvars.Scope, envSource string, read map[envvars.Cell]Value, folders []string, keep []envvars.Cell) (CopyResult, error) {
	result := CopyResult{EnvSource: envSource}
	listed, err := store.List(ctx, scope)
	if err != nil {
		return CopyResult{}, err
	}
	stored := map[envvars.Cell]envvars.Metadata{}
	for _, metadata := range listed {
		if metadata.Coordinate.Environment == "" {
			stored[metadata.Coordinate.Cell] = metadata
		}
	}
	copied := func(at envvars.Cell) bool {
		return slices.Contains(folders, at.Folder) && !slices.Contains(keep, at)
	}

	for _, at := range slices.SortedFunc(maps.Keys(read), envvars.Cell.Compare) {
		if !copied(at) {
			continue
		}
		result.Present = append(result.Present, at)
		next := read[at]
		current, found := stored[at]
		if found && current.Provenance.EnvSource == envSource && !isNewerVersion(next.Version, current.Provenance.Version) {
			result.Unchanged = append(result.Unchanged, at)
			continue
		}
		var expected int64
		if found {
			expected = current.Version
		}
		_, err := store.SetFromEnvSource(ctx, scope, envvars.Coordinate{Cell: at}, string(next.Plaintext), envvars.Provenance{EnvSource: envSource, Version: next.Version}, expected)
		switch {
		case err == nil:
			result.Written = append(result.Written, at)
		case errors.Is(err, envvars.ErrStaleVersion):
		case errors.Is(err, envvars.ErrTooLarge), errors.Is(err, envvars.ErrIsReference):
			result.refuse(at, err)
		default:
			return CopyResult{}, err
		}
	}

	for _, metadata := range listed {
		at := metadata.Coordinate.Cell
		if metadata.Coordinate.Environment != "" || metadata.Target != nil || !copied(at) {
			continue
		}
		if _, present := read[at]; present {
			continue
		}
		expected := metadata.Version
		removed, err := store.Delete(ctx, scope, metadata.Coordinate, &expected)
		if errors.Is(err, envvars.ErrStaleVersion) {
			continue
		}
		if err != nil {
			return CopyResult{}, err
		}
		if removed {
			result.Removed = append(result.Removed, at)
		}
	}
	return result, nil
}

func ClearProvenance(ctx context.Context, store envvars.Store, scope envvars.Scope) error {
	listed, err := store.List(ctx, scope)
	if err != nil {
		return err
	}
	for _, metadata := range listed {
		if metadata.Coordinate.Environment != "" || metadata.Target != nil || metadata.Provenance.EnvSource == "" {
			continue
		}
		copied, err := store.Get(ctx, scope, metadata.Coordinate, true)
		if errors.Is(err, envvars.ErrNotFound) {
			continue
		}
		if err != nil {
			return err
		}
		expected := copied.Version
		if _, err := store.Set(ctx, scope, metadata.Coordinate, copied.Plaintext, &expected); err != nil && !errors.Is(err, envvars.ErrStaleVersion) {
			return err
		}
	}
	return nil
}

func (r *CopyResult) refuse(at envvars.Cell, err error) {
	if r.Refused == nil {
		r.Refused = map[envvars.Cell]string{}
	}
	r.Refused[at] = err.Error()
}

func isNewerVersion(read, stored string) bool {
	if read == stored {
		return false
	}
	readID, readNumber, readNumbered := numberedVersion(read)
	storedID, storedNumber, storedNumbered := numberedVersion(stored)
	if readNumbered && storedNumbered && readID == storedID {
		return readNumber > storedNumber
	}
	return true
}

func numberedVersion(version string) (string, int64, bool) {
	id, number, split := strings.Cut(version, "@")
	if !split {
		return "", 0, false
	}
	parsed, err := strconv.ParseInt(number, 10, 64)
	return id, parsed, err == nil
}
