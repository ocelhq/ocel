package envvars

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/seal"
)

const (
	MaxValueBytes = 4096

	historyWindow = 50
)

var (
	ErrNotFound = errors.New("envvars: not found")

	ErrStaleVersion = errors.New("envvars: stale version")

	ErrIsReference = errors.New("envvars: cell is a reference")

	ErrDangling = errors.New("envvars: reference to a value that is not there")

	ErrWouldDeepen = errors.New("envvars: reference to a reference")

	ErrTooLarge = errors.New("envvars: value is too large")
)

type Store struct {
	KeyValues keyvalue.Store
	Cipher    seal.Cipher
	Now       func() time.Time
}

type Metadata struct {
	Coordinate Coordinate
	Version    int64
	UpdatedAt  int64
	Size       int64
	Target     *Target
	Provenance Provenance
}

type Target struct {
	Project string
	Cell
}

type Value struct {
	Metadata
	Plaintext string
}

type Version struct {
	Version   int64
	CreatedAt int64
	Size      int64
}

type storedValue struct {
	Version   int64   `json:"version"`
	UpdatedAt int64   `json:"updatedAt"`
	Size      int64   `json:"size"`
	Sealed    []byte  `json:"sealed,omitempty"`
	Deleted   bool    `json:"deleted,omitempty"`
	Target    *Target `json:"target,omitempty"`
	Provenance
}

func (c storedValue) live() int64 {
	if c.Deleted {
		return 0
	}
	return c.Version
}

func (s Store) now() int64 {
	if s.Now == nil {
		return time.Now().Unix()
	}
	return s.Now().Unix()
}

func (s Store) Set(ctx context.Context, scope Scope, at Coordinate, plaintext string, expected *int64) (Metadata, error) {
	return s.write(ctx, scope, at, plaintext, Provenance{}, expected)
}

func (s Store) write(ctx context.Context, scope Scope, at Coordinate, plaintext string, from Provenance, expected *int64) (Metadata, error) {
	if len(plaintext) > MaxValueBytes {
		return Metadata{}, fmt.Errorf("value for %s is too large: %d bytes, limit %d: %w", at.Key, len(plaintext), MaxValueBytes, ErrTooLarge)
	}
	recorded, current, err := s.cellAt(ctx, scope, at)
	if err != nil {
		return Metadata{}, err
	}
	if current.Target != nil {
		return Metadata{}, fmt.Errorf("%s is a reference to %s, which is where that value is edited: %w", at, current.Target, ErrIsReference)
	}
	if from.isOlderThan(current.Provenance) {
		return Metadata{}, ErrStaleVersion
	}

	bound, err := newCellAssociatedData(scope, at)
	if err != nil {
		return Metadata{}, err
	}
	sealed, err := s.Cipher.Seal(ctx, scope.Tier, bound, []byte(plaintext))
	if err != nil {
		return Metadata{}, err
	}
	return s.commit(ctx, scope, at, recorded, current, expected, storedValue{Sealed: sealed, Size: int64(len(plaintext)), Provenance: from})
}

func (s Store) commit(ctx context.Context, scope Scope, at Coordinate, recorded keyvalue.Entry, current storedValue, expected *int64, next storedValue) (Metadata, error) {
	if expected != nil && *expected != current.live() {
		return Metadata{}, ErrStaleVersion
	}

	next.Version = current.Version + 1
	next.UpdatedAt = s.now()

	encoded, err := json.Marshal(next)
	if err != nil {
		return Metadata{}, fmt.Errorf("encode %s: %w", at, err)
	}
	recorded.Value = encoded
	if _, err := s.KeyValues.Write(ctx, recorded); err != nil {
		if errors.Is(err, keyvalue.ErrStale) {
			return Metadata{}, ErrStaleVersion
		}
		return Metadata{}, fmt.Errorf("write %s: %w", at.Key, err)
	}
	if err := s.remember(ctx, scope, at, next); err != nil {
		return Metadata{}, err
	}
	return metadataOf(at, next), nil
}

func (s Store) remember(ctx context.Context, scope Scope, at Coordinate, written storedValue) error {
	entry, err := json.Marshal(Version{Version: written.Version, CreatedAt: written.UpdatedAt, Size: written.Size})
	if err != nil {
		return fmt.Errorf("encode version %d of %s: %w", written.Version, at, err)
	}
	recorded, err := keyvalue.ReadOrEmpty(ctx, s.KeyValues, versionKey(scope, at, written.Version))
	if err != nil {
		return err
	}
	recorded.Value = entry
	if _, err := s.KeyValues.Write(ctx, recorded); err != nil && !errors.Is(err, keyvalue.ErrStale) {
		return fmt.Errorf("record version %d of %s: %w", written.Version, at, err)
	}
	if pruned := written.Version - historyWindow; pruned > 0 {
		if err := keyvalue.Forget(ctx, s.KeyValues, versionKey(scope, at, pruned)); err != nil {
			return fmt.Errorf("drop version %d of %s: %w", pruned, at, err)
		}
	}
	return nil
}

func (s Store) Get(ctx context.Context, scope Scope, at Coordinate, reveal bool) (Value, error) {
	_, cell, err := s.cellAt(ctx, scope, at)
	if err != nil {
		return Value{}, err
	}
	if cell.live() == 0 {
		return Value{}, ErrNotFound
	}
	value := Value{Metadata: metadataOf(at, cell)}
	if !reveal {
		return value, nil
	}
	plaintext, err := s.open(ctx, scope, at, cell)
	if err != nil {
		return Value{}, err
	}
	value.Plaintext = plaintext
	return value, nil
}

func (s Store) open(ctx context.Context, scope Scope, at Coordinate, cell storedValue) (string, error) {
	source, from, sourceAt, err := s.dereference(ctx, scope, at, cell)
	if err != nil {
		return "", err
	}
	bound, err := newCellAssociatedData(from, sourceAt)
	if err != nil {
		return "", err
	}
	plaintext, err := s.Cipher.Open(ctx, from.Tier, bound, source.Sealed)
	if err != nil {
		return "", err
	}
	return string(plaintext), nil
}

func (s Store) dereference(ctx context.Context, scope Scope, at Coordinate, cell storedValue) (storedValue, Scope, Coordinate, error) {
	if cell.Target == nil {
		return cell, scope, at, nil
	}
	from := Scope{Project: cell.Target.Project, Tier: scope.Tier}
	sourceAt := Coordinate{Cell: cell.Target.Cell}
	_, source, err := s.cellAt(ctx, from, sourceAt)
	if err != nil {
		return storedValue{}, Scope{}, Coordinate{}, err
	}
	if err := validateSource(at, cell.Target, source); err != nil {
		return storedValue{}, Scope{}, Coordinate{}, err
	}
	return source, from, sourceAt, nil
}

func validateSource(at Coordinate, target *Target, source storedValue) error {
	switch {
	case source.live() == 0:
		return fmt.Errorf("%s references %s, which has no value: %w", at, target, ErrDangling)
	case source.Target != nil:
		return fmt.Errorf("%s references %s, which is itself a reference: %w", at, target, ErrWouldDeepen)
	}
	return nil
}

func (s Store) List(ctx context.Context, scope Scope) ([]Metadata, error) {
	recorded, err := s.listUnder(ctx, cellsPrefix(scope))
	if err != nil {
		return nil, fmt.Errorf("read %s's values: %w", scope.Project, err)
	}
	out := make([]Metadata, 0, len(recorded))
	for _, entry := range recorded {
		at, ok := cellOf(entry.Key)
		if !ok {
			continue
		}
		var stored storedValue
		if err := json.Unmarshal(entry.Value, &stored); err != nil {
			return nil, fmt.Errorf("read %s: %w", at, err)
		}
		if stored.Deleted {
			continue
		}
		out = append(out, metadataOf(at, stored))
	}
	slices.SortFunc(out, func(a, b Metadata) int {
		return strings.Compare(a.Coordinate.String(), b.Coordinate.String())
	})
	return out, nil
}

func (s Store) Reveal(ctx context.Context, scope Scope, cells []Coordinate) ([]Value, error) {
	if len(cells) == 0 {
		return nil, nil
	}
	stored, err := s.storedCells(ctx, scope)
	if err != nil {
		return nil, err
	}

	found := make([]Coordinate, 0, len(cells))
	cellValues := make([]storedValue, 0, len(cells))
	for _, at := range cells {
		cell, ok := stored[cellKey(scope, at).String()]
		if !ok || cell.live() == 0 {
			continue
		}
		found = append(found, at)
		cellValues = append(cellValues, cell)
	}

	sources, err := s.gather(ctx, scope, stored, cellValues)
	if err != nil {
		return nil, err
	}

	sealed := make([]storedValue, len(found))
	from := make([]Scope, len(found))
	sourceCells := make([]Coordinate, len(found))
	for i, at := range found {
		if cellValues[i].Target == nil {
			sealed[i], from[i], sourceCells[i] = cellValues[i], scope, at
			continue
		}
		target := *cellValues[i].Target
		from[i] = Scope{Project: target.Project, Tier: scope.Tier}
		sourceCells[i] = Coordinate{Cell: target.Cell}
		source := sources[cellKey(from[i], sourceCells[i]).String()]
		if err := validateSource(at, &target, source); err != nil {
			return nil, err
		}
		sealed[i] = source
	}

	opening := make([]int, 0, len(found))
	slotOf := make([]int, len(found))
	slots := make(map[string]int, len(found))
	for i := range found {
		key := cellKey(from[i], sourceCells[i]).String()
		slot, seen := slots[key]
		if !seen {
			slot = len(opening)
			slots[key] = slot
			opening = append(opening, i)
		}
		slotOf[i] = slot
	}

	plaintexts := make([]string, len(opening))
	if err := forEachConcurrently(ctx, len(opening), func(ctx context.Context, slot int) error {
		i := opening[slot]
		bound, err := newCellAssociatedData(from[i], sourceCells[i])
		if err != nil {
			return err
		}
		plaintext, err := s.Cipher.Open(ctx, from[i].Tier, bound, sealed[i].Sealed)
		if err != nil {
			return err
		}
		plaintexts[slot] = string(plaintext)
		return nil
	}); err != nil {
		return nil, err
	}

	out := make([]Value, 0, len(found))
	for i, at := range found {
		out = append(out, Value{Metadata: metadataOf(at, cellValues[i]), Plaintext: plaintexts[slotOf[i]]})
	}
	return out, nil
}

func (s Store) storedCells(ctx context.Context, scope Scope) (map[string]storedValue, error) {
	recorded, err := s.listUnder(ctx, cellsPrefix(scope))
	if err != nil {
		return nil, fmt.Errorf("read %s's values: %w", scope.Project, err)
	}
	out := make(map[string]storedValue, len(recorded))
	for _, entry := range recorded {
		var stored storedValue
		if err := json.Unmarshal(entry.Value, &stored); err != nil {
			return nil, fmt.Errorf("read %s: %w", entry.Key, err)
		}
		out[entry.Key.String()] = stored
	}
	return out, nil
}

func (s Store) gather(ctx context.Context, scope Scope, stored map[string]storedValue, cellValues []storedValue) (map[string]storedValue, error) {
	var from []Scope
	var sourceCells []Coordinate
	wanted := map[string]bool{}
	for _, cell := range cellValues {
		if cell.Target == nil {
			continue
		}
		at := Scope{Project: cell.Target.Project, Tier: scope.Tier}
		sourceAt := Coordinate{Cell: cell.Target.Cell}
		key := cellKey(at, sourceAt).String()
		if wanted[key] {
			continue
		}
		wanted[key] = true
		from = append(from, at)
		sourceCells = append(sourceCells, sourceAt)
	}

	sources := make([]storedValue, len(from))
	if err := forEachConcurrently(ctx, len(from), func(ctx context.Context, i int) error {
		if from[i].Project == scope.Project {
			sources[i] = stored[cellKey(from[i], sourceCells[i]).String()]
			return nil
		}
		_, source, err := s.cellAt(ctx, from[i], sourceCells[i])
		sources[i] = source
		return err
	}); err != nil {
		return nil, err
	}

	out := make(map[string]storedValue, len(from))
	for i := range from {
		out[cellKey(from[i], sourceCells[i]).String()] = sources[i]
	}
	return out, nil
}

func (s Store) Delete(ctx context.Context, scope Scope, at Coordinate, expected *int64) (bool, error) {
	return s.remove(ctx, scope, at, Provenance{}, expected)
}

func (s Store) remove(ctx context.Context, scope Scope, at Coordinate, from Provenance, expected *int64) (bool, error) {
	recorded, current, err := s.cellAt(ctx, scope, at)
	if err != nil {
		return false, err
	}
	if expected != nil && *expected != current.live() || from.isOlderThan(current.Provenance) {
		return false, ErrStaleVersion
	}
	if current.live() == 0 {
		return false, nil
	}
	if current.Target != nil {
		if err := s.unindexReference(ctx, scope, at, current.Target); err != nil {
			return false, err
		}
	}

	tombstone := storedValue{Version: current.Version, UpdatedAt: s.now(), Deleted: true, Provenance: from}
	encoded, err := json.Marshal(tombstone)
	if err != nil {
		return false, fmt.Errorf("encode %s: %w", at, err)
	}
	recorded.Value = encoded
	if _, err := s.KeyValues.Write(ctx, recorded); err != nil {
		if errors.Is(err, keyvalue.ErrStale) {
			return false, ErrStaleVersion
		}
		return false, fmt.Errorf("delete %s: %w", at.Key, err)
	}
	return true, nil
}

func (s Store) Versions(ctx context.Context, scope Scope, at Coordinate) ([]Version, error) {
	recorded, err := s.listUnder(ctx, historyPrefix(scope, at))
	if err != nil {
		return nil, fmt.Errorf("read %s's versions: %w", at, err)
	}
	out := make([]Version, 0, len(recorded))
	for _, entry := range recorded {
		var version Version
		if err := json.Unmarshal(entry.Value, &version); err != nil {
			return nil, fmt.Errorf("read a version of %s: %w", at, err)
		}
		out = append(out, version)
	}
	slices.SortFunc(out, func(a, b Version) int { return int(a.Version - b.Version) })
	return out, nil
}

func (s Store) Purge(ctx context.Context, scope Scope) (int, error) {
	borrowed, err := s.List(ctx, scope)
	if err != nil {
		return 0, err
	}
	for _, m := range borrowed {
		if m.Target == nil {
			continue
		}
		if err := s.unindexReference(ctx, scope, m.Coordinate, m.Target); err != nil {
			return 0, err
		}
	}

	recorded, err := s.KeyValues.List(ctx, ValuesPartition(scope))
	if err != nil {
		return 0, fmt.Errorf("read %s's values: %w", scope.Project, err)
	}
	for _, entry := range recorded {
		if err := keyvalue.Forget(ctx, s.KeyValues, entry.Key); err != nil {
			return 0, fmt.Errorf("remove %s's stored values: %w", scope.Project, err)
		}
	}
	refs, err := s.KeyValues.List(ctx, ReferencesPartition(scope))
	if err != nil {
		return 0, fmt.Errorf("read what references %s: %w", scope.Project, err)
	}
	for _, entry := range refs {
		if err := keyvalue.Forget(ctx, s.KeyValues, entry.Key); err != nil {
			return 0, fmt.Errorf("remove what references %s: %w", scope.Project, err)
		}
	}
	return len(recorded), nil
}

func (s Store) cellAt(ctx context.Context, scope Scope, at Coordinate) (keyvalue.Entry, storedValue, error) {
	recorded, err := keyvalue.ReadOrEmpty(ctx, s.KeyValues, cellKey(scope, at))
	if err != nil {
		return keyvalue.Entry{}, storedValue{}, fmt.Errorf("read %s: %w", at, err)
	}
	if len(recorded.Value) == 0 {
		return recorded, storedValue{}, nil
	}
	var stored storedValue
	if err := json.Unmarshal(recorded.Value, &stored); err != nil {
		return keyvalue.Entry{}, storedValue{}, fmt.Errorf("read %s: %w", at, err)
	}
	return recorded, stored, nil
}

func metadataOf(at Coordinate, cell storedValue) Metadata {
	return Metadata{
		Coordinate: at,
		Version:    cell.Version,
		UpdatedAt:  cell.UpdatedAt,
		Size:       cell.Size,
		Target:     cell.Target,
		Provenance: cell.Provenance,
	}
}

func (t *Target) String() string {
	if t == nil {
		return ""
	}
	out := t.Project + "/" + t.Key
	if t.Folder != "" && t.Folder != rootFolder {
		out += " in " + t.Folder
	}
	return out
}
