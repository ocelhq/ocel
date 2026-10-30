package envsource

import (
	"context"
	"errors"
	"fmt"
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

	environment string
	versions    map[variablestore.Cell]variablestore.Version
}

func CopyValues(ctx context.Context, store variablestore.Store, scope variablestore.Scope, environment string, listed []variablestore.Metadata, key DigestKey, envSource string, readAt time.Time, read map[variablestore.Cell]Value, folders []string, keep []variablestore.Cell) (CopyResult, error) {
	result := CopyResult{EnvSource: envSource, environment: environment}
	stored := map[variablestore.Cell]variablestore.Metadata{}
	tierWide := map[variablestore.Cell]variablestore.Metadata{}
	for _, metadata := range listed {
		if metadata.Coordinate.Environment == environment {
			stored[metadata.Coordinate.Cell] = metadata
		}
		if metadata.Coordinate.Environment == "" {
			tierWide[metadata.Coordinate.Cell] = metadata
		}
	}
	copied := func(at variablestore.Cell) bool {
		return slices.Contains(folders, at.Folder) && !slices.Contains(keep, at)
	}
	holds := func(metadata variablestore.Metadata, found bool, version string) bool {
		return found && metadata.Provenance.EnvSource == envSource && !isNewerVersion(version, metadata.Provenance.Version)
	}
	from := variablestore.Provenance{EnvSource: envSource, ReadAt: readAt}

	for _, at := range slices.SortedFunc(maps.Keys(read), variablestore.Cell.Compare) {
		if !copied(at) {
			continue
		}
		result.Present = append(result.Present, at)
		next := read[at]
		version, err := key.version(scope, at, next)
		if err != nil {
			return result, err
		}
		current, found := stored[at]
		if environment != "" {
			tierWideValue, isTierWide := tierWide[at]
			switch {
			case found && isSetByHand(current):
				continue
			case found && current.Provenance.Publisher != "":
			case holds(tierWideValue, isTierWide, version):
				if found {
					if err := removeCopy(ctx, store, scope, current, from); err != nil {
						return result, err
					}
				}
				result.Unchanged = append(result.Unchanged, at)
				continue
			}
		}
		if holds(current, found, version) {
			result.Unchanged = append(result.Unchanged, at)
			continue
		}
		var expected int64
		if found {
			expected = current.Version
		}
		written, err := store.SetFromEnvSource(ctx, scope, variablestore.Coordinate{Cell: at, Environment: environment}, string(next.Plaintext), variablestore.Provenance{EnvSource: envSource, Version: version, ReadAt: readAt}, expected)
		if written.Version > 0 {
			result.recordVersion(at, variablestore.Version{Version: written.Version, CreatedAt: written.UpdatedAt, Size: written.Size})
		}
		var published *variablestore.PublishedError
		switch {
		case err == nil:
			result.Written = append(result.Written, at)
		case errors.As(err, &published):
			result.refuse(at, fmt.Errorf("%w, so %s never writes it: stop publishing it there to read it from %s", err, envSource, envSource))
		case errors.Is(err, variablestore.ErrStaleVersion):
		case errors.Is(err, variablestore.ErrTooLarge), errors.Is(err, variablestore.ErrIsReference):
			result.refuse(at, err)
		default:
			return result, err
		}
	}

	for _, metadata := range listed {
		at := metadata.Coordinate.Cell
		if metadata.Coordinate.Environment != environment || metadata.Target != nil || metadata.Provenance.Publisher != "" || !copied(at) {
			continue
		}
		if environment != "" && isSetByHand(metadata) {
			continue
		}
		if _, present := read[at]; present {
			continue
		}
		removed, err := store.DeleteFromEnvSource(ctx, scope, metadata.Coordinate, from, metadata.Version)
		if errors.Is(err, variablestore.ErrStaleVersion) {
			continue
		}
		if err != nil {
			return result, err
		}
		if removed {
			result.Removed = append(result.Removed, at)
		}
	}
	return result, nil
}

func isSetByHand(metadata variablestore.Metadata) bool {
	return metadata.Provenance.EnvSource == "" && metadata.Provenance.Publisher == ""
}

func removeCopy(ctx context.Context, store variablestore.Store, scope variablestore.Scope, copied variablestore.Metadata, from variablestore.Provenance) error {
	_, err := store.DeleteFromEnvSource(ctx, scope, copied.Coordinate, from, copied.Version)
	if errors.Is(err, variablestore.ErrStaleVersion) {
		return nil
	}
	return err
}

func ClearProvenance(ctx context.Context, store variablestore.Store, scope variablestore.Scope, environment string) error {
	listed, err := store.List(ctx, scope)
	if err != nil {
		return err
	}
	for _, metadata := range listed {
		if metadata.Coordinate.Environment != environment || metadata.Target != nil || metadata.Provenance.EnvSource == "" {
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

func (r *CopyResult) recordVersion(at variablestore.Cell, version variablestore.Version) {
	if r.versions == nil {
		r.versions = map[variablestore.Cell]variablestore.Version{}
	}
	r.versions[at] = version
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
