package variablestore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/ocelhq/ocel/pkg/records"
)

type Class int

const (
	ClassUnstated Class = iota
	ClassPlain
	ClassSensitive
	ClassSecret
)

func (c Class) String() string {
	switch c {
	case ClassUnstated:
		return "unstated"
	case ClassPlain:
		return "plain"
	case ClassSensitive:
		return "sensitive"
	case ClassSecret:
		return "secret"
	}
	return fmt.Sprintf("class %d", int(c))
}

func (c Class) orSensitive() Class {
	if c == ClassUnstated {
		return ClassSensitive
	}
	return c
}

func (c storedValue) classAgainst(source storedValue) Class {
	if c.Target == nil {
		return c.Class.orSensitive()
	}
	return max(c.Class, source.Class.orSensitive())
}

func (c Class) overwrittenBy(written Class) Class {
	if written == ClassUnstated {
		return c
	}
	return written
}

func (c Class) classifiedAs(declared Class) Class {
	if c == ClassUnstated {
		return declared
	}
	return max(c, declared)
}

func (s Store) Classify(ctx context.Context, scope Scope, declared map[Cell]Class) ([]Metadata, error) {
	if len(declared) == 0 {
		return nil, nil
	}
	wanted := make(map[Cell]Class, len(declared))
	for cell, class := range declared {
		if class == ClassUnstated {
			return nil, fmt.Errorf("%s is classified with no class", Coordinate{Cell: cell})
		}
		wanted[Coordinate{Cell: cell}.canonical().Cell] = class
	}
	recorded, err := s.listUnder(ctx, cellsPrefix(scope))
	if err != nil {
		return nil, fmt.Errorf("read %s's values: %w", scope.Project, err)
	}
	var raised []Metadata
	for _, entry := range recorded {
		at, ok := cellOf(entry.Key)
		if !ok {
			continue
		}
		class, named := wanted[at.canonical().Cell]
		if !named {
			continue
		}
		var stored storedValue
		if err := json.Unmarshal(entry.Value, &stored); err != nil {
			return nil, fmt.Errorf("read %s: %w", at, err)
		}
		if stored.live() == 0 || stored.Class.classifiedAs(class) == stored.Class {
			continue
		}
		metadata, changed, err := s.classify(ctx, scope, at, entry, stored, class)
		if err != nil {
			return nil, err
		}
		if changed {
			raised = append(raised, metadata)
		}
	}
	return raised, nil
}

func (s Store) classify(ctx context.Context, scope Scope, at Coordinate, recorded records.Entry, current storedValue, declared Class) (Metadata, bool, error) {
	changed := false
	first := true
	err := records.RetryWhileStale(ctx, func() (bool, error) {
		if !first {
			var err error
			if recorded, current, err = s.cellAt(ctx, scope, at); err != nil {
				return false, err
			}
		}
		first = false
		next := current.Class.classifiedAs(declared)
		if current.live() == 0 || next == current.Class {
			return false, nil
		}
		current.Class = next
		encoded, err := json.Marshal(current)
		if err != nil {
			return false, fmt.Errorf("encode %s: %w", at, err)
		}
		recorded.Value = encoded
		if _, err := s.KeyValues.Write(ctx, recorded); err != nil {
			if errors.Is(err, records.ErrStale) {
				return true, nil
			}
			return false, fmt.Errorf("classify %s: %w", at, err)
		}
		changed = true
		return false, nil
	})
	if errors.Is(err, records.ErrContended) {
		return Metadata{}, false, fmt.Errorf("%s kept changing under every attempt to classify it; run the deploy again", at)
	}
	if err != nil || !changed {
		return Metadata{}, false, err
	}
	source, err := s.sourceOf(ctx, scope, current)
	if err != nil {
		return Metadata{}, false, err
	}
	return metadataAgainst(at, current, source), true, nil
}
