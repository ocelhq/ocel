package envsource

import (
	"context"
	"errors"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ocelhq/ocel/pkg/variablestore"
)

type CopyResult struct {
	EnvSource string
	Present   []variablestore.Cell
	Written   []variablestore.Cell
	Unchanged []variablestore.Cell
	Removed   []variablestore.Cell
	Refused   map[variablestore.Cell]string
}

func CopyValues(ctx context.Context, store variablestore.Store, scope variablestore.Scope, key DigestKey, envSource string, readAt time.Time, read map[variablestore.Cell]Value, folders []string, keep []variablestore.Cell) (CopyResult, error) {
	result := CopyResult{EnvSource: envSource}
	listed, err := store.List(ctx, scope)
	if err != nil {
		return CopyResult{}, err
	}
	stored := map[variablestore.Cell]variablestore.Metadata{}
	for _, metadata := range listed {
		if metadata.Coordinate.Environment == "" {
			stored[metadata.Coordinate.Cell] = metadata
		}
	}
	copied := func(at variablestore.Cell) bool {
		return slices.Contains(folders, at.Folder) && !slices.Contains(keep, at)
	}

	for _, at := range slices.SortedFunc(maps.Keys(read), variablestore.Cell.Compare) {
		if !copied(at) {
			continue
		}
		result.Present = append(result.Present, at)
		next := read[at]
		version, err := key.version(scope, at, next)
		if err != nil {
			return CopyResult{}, err
		}
		current, found := stored[at]
		if found && current.Provenance.EnvSource == envSource && !isNewerVersion(version, current.Provenance.Version) {
			result.Unchanged = append(result.Unchanged, at)
			continue
		}
		var expected int64
		if found {
			expected = current.Version
		}
		_, err = store.SetFromEnvSource(ctx, scope, variablestore.Coordinate{Cell: at}, string(next.Plaintext), variablestore.Provenance{EnvSource: envSource, Version: version, ReadAt: readAt}, expected)
		switch {
		case err == nil:
			result.Written = append(result.Written, at)
		case errors.Is(err, variablestore.ErrStaleVersion):
		case errors.Is(err, variablestore.ErrTooLarge), errors.Is(err, variablestore.ErrIsReference):
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
		removed, err := store.DeleteFromEnvSource(ctx, scope, metadata.Coordinate, variablestore.Provenance{EnvSource: envSource, ReadAt: readAt}, metadata.Version)
		if errors.Is(err, variablestore.ErrStaleVersion) {
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

func ClearProvenance(ctx context.Context, store variablestore.Store, scope variablestore.Scope) error {
	listed, err := store.List(ctx, scope)
	if err != nil {
		return err
	}
	for _, metadata := range listed {
		if metadata.Coordinate.Environment != "" || metadata.Target != nil || metadata.Provenance.EnvSource == "" {
			continue
		}
		copied, err := store.Get(ctx, scope, metadata.Coordinate, true)
		if errors.Is(err, variablestore.ErrNotFound) {
			continue
		}
		if err != nil {
			return err
		}
		expected := copied.Version
		if _, err := store.Set(ctx, scope, metadata.Coordinate, copied.Plaintext, &expected); err != nil && !errors.Is(err, variablestore.ErrStaleVersion) {
			return err
		}
	}
	return nil
}

func (r *CopyResult) refuse(at variablestore.Cell, err error) {
	if r.Refused == nil {
		r.Refused = map[variablestore.Cell]string{}
	}
	r.Refused[at] = err.Error()
}

func isNewerVersion(read, stored string) bool {
	if read == stored {
		return false
	}
	readID, readNumber, readNumbered := numberedVersion(read)
	storedID, storedNumber, storedNumbered := numberedVersion(stored)
	if readNumbered && storedNumbered && readID == storedID && readNumber != storedNumber {
		return readNumber > storedNumber
	}
	return true
}

func numberedVersion(version string) (string, int64, bool) {
	id, number, split := strings.Cut(version, "@")
	if !split {
		return "", 0, false
	}
	number, _, _ = strings.Cut(number, "#")
	parsed, err := strconv.ParseInt(number, 10, 64)
	return id, parsed, err == nil
}
