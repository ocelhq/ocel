package variablestore

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/ocelhq/ocel/pkg/records"
)

type Provenance struct {
	EnvSource string    `json:"envSource,omitempty"`
	Version   string    `json:"envSourceVersion,omitempty"`
	ReadAt    time.Time `json:"envSourceReadAt,omitzero"`

	Publisher       string `json:"publisher,omitempty"`
	PublishedSecret bool   `json:"publishedSecret,omitempty"`
}

func (p Provenance) isOlderThan(stored Provenance) bool {
	return p.EnvSource != "" && p.EnvSource == stored.EnvSource && p.ReadAt.Before(stored.ReadAt)
}

func (s Store) SetFromEnvSource(ctx context.Context, scope Scope, at Coordinate, plaintext string, from Provenance, expected int64) (Metadata, error) {
	return s.write(ctx, scope, at, plaintext, from, &expected)
}

func (s Store) DeleteFromEnvSource(ctx context.Context, scope Scope, at Coordinate, from Provenance, expected int64) (bool, error) {
	return s.remove(ctx, scope, at, from, &expected)
}

func (s Store) ForgetFromEnvSource(ctx context.Context, scope Scope, at Coordinate, from Provenance, written Version) error {
	found, matched := false, false
	err := records.ForgetMatching(ctx, s.KeyValues, CellKey(scope, at), func(recorded records.Entry) (bool, error) {
		var stored storedValue
		if err := json.Unmarshal(recorded.Value, &stored); err != nil {
			return false, fmt.Errorf("read %s: %w", at, err)
		}
		found, matched = true, stored.EnvSource == from.EnvSource && stored.ReadAt.Equal(from.ReadAt)
		if matched {
			written = Version{Version: stored.Version, CreatedAt: stored.UpdatedAt}
		}
		return matched, nil
	})
	if err != nil || (found && !matched) || written.Version == 0 {
		return err
	}
	return records.ForgetMatching(ctx, s.KeyValues, versionKey(scope, at, written.Version), func(recorded records.Entry) (bool, error) {
		var version Version
		if err := json.Unmarshal(recorded.Value, &version); err != nil {
			return false, fmt.Errorf("read version %d of %s: %w", written.Version, at, err)
		}
		return version.Version == written.Version && version.CreatedAt == written.CreatedAt, nil
	})
}

type Dereferenced struct {
	Project string
	Metadata
	Plaintext string
}

func (s Store) GetDereferenced(ctx context.Context, scope Scope, at Coordinate, reveal bool) (Dereferenced, error) {
	_, cell, err := s.cellAt(ctx, scope, at)
	if err != nil {
		return Dereferenced{}, err
	}
	if cell.live() == 0 {
		return Dereferenced{}, ErrNotFound
	}
	source, from, sourceAt, err := s.dereference(ctx, scope, at, cell)
	if err != nil {
		return Dereferenced{}, err
	}
	out := Dereferenced{Project: from.Project, Metadata: metadataOf(sourceAt, source)}
	if !reveal {
		return out, nil
	}
	bound, err := newCellAssociatedData(from, sourceAt)
	if err != nil {
		return Dereferenced{}, err
	}
	plaintext, err := s.Cipher.Open(ctx, from.Tier, bound, source.Sealed)
	if err != nil {
		return Dereferenced{}, err
	}
	out.Plaintext = string(plaintext)
	return out, nil
}
