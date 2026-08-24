package values

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/ocelhq/ocel/pkg/providerkit/ports"
)

const (
	MaxValueBytes = 4096

	historyWindow = 50
)

var (
	ErrNotFound = errors.New("values: not found")

	ErrStaleVersion = errors.New("values: stale version")

	ErrIsReference = errors.New("values: cell is a reference")

	ErrDangling = errors.New("values: reference to a value that is not there")

	ErrWouldDeepen = errors.New("values: reference to a reference")

	ErrTooLarge = errors.New("values: value is too large")
)

type Store struct {
	Records ports.RecordStore
	Sealer  ports.Sealer
	Now     func() time.Time
}

type Metadata struct {
	Coordinate Coordinate
	Version    int64
	UpdatedAt  int64
	Size       int64
	Target     *Target
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

type cell struct {
	Version   int64   `json:"version"`
	UpdatedAt int64   `json:"updatedAt"`
	Size      int64   `json:"size"`
	Sealed    []byte  `json:"sealed,omitempty"`
	Deleted   bool    `json:"deleted,omitempty"`
	Target    *Target `json:"target,omitempty"`
}

func (c cell) live() int64 {
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
	if len(plaintext) > MaxValueBytes {
		return Metadata{}, fmt.Errorf("value for %s is too large: %d bytes, limit %d: %w", at.Key, len(plaintext), MaxValueBytes, ErrTooLarge)
	}
	held, current, err := s.cellAt(ctx, scope, at)
	if err != nil {
		return Metadata{}, err
	}
	if current.Target != nil {
		return Metadata{}, fmt.Errorf("%s is a reference to %s, which is where that value is edited: %w", at, current.Target, ErrIsReference)
	}

	sealed, err := s.Sealer.Seal(ctx, coordinateOf(scope, at), []byte(plaintext))
	if err != nil {
		return Metadata{}, err
	}
	return s.commit(ctx, scope, at, held, current, expected, cell{Sealed: sealed, Size: int64(len(plaintext))})
}

func (s Store) commit(ctx context.Context, scope Scope, at Coordinate, held ports.Record, current cell, expected *int64, next cell) (Metadata, error) {
	if expected != nil && *expected != current.live() {
		return Metadata{}, ErrStaleVersion
	}

	next.Version = current.Version + 1
	next.UpdatedAt = s.now()

	encoded, err := json.Marshal(next)
	if err != nil {
		return Metadata{}, fmt.Errorf("encode %s: %w", at, err)
	}
	held.Bytes = encoded
	if _, err := s.Records.Write(ctx, held); err != nil {
		if errors.Is(err, ports.ErrStale) {
			return Metadata{}, ErrStaleVersion
		}
		return Metadata{}, fmt.Errorf("write %s: %w", at.Key, err)
	}
	if err := s.remember(ctx, scope, at, next); err != nil {
		return Metadata{}, err
	}
	return metadataOf(at, next), nil
}

func (s Store) remember(ctx context.Context, scope Scope, at Coordinate, written cell) error {
	entry, err := json.Marshal(Version{Version: written.Version, CreatedAt: written.UpdatedAt, Size: written.Size})
	if err != nil {
		return fmt.Errorf("encode version %d of %s: %w", written.Version, at, err)
	}
	held, err := ports.Held(ctx, s.Records, versionName(scope, at, written.Version))
	if err != nil {
		return err
	}
	held.Bytes = entry
	if _, err := s.Records.Write(ctx, held); err != nil && !errors.Is(err, ports.ErrStale) {
		return fmt.Errorf("record version %d of %s: %w", written.Version, at, err)
	}
	if pruned := written.Version - historyWindow; pruned > 0 {
		if err := ports.Forget(ctx, s.Records, versionName(scope, at, pruned)); err != nil {
			return fmt.Errorf("drop version %d of %s: %w", pruned, at, err)
		}
	}
	return nil
}

func (s Store) Get(ctx context.Context, scope Scope, at Coordinate, reveal bool) (Value, error) {
	_, held, err := s.cellAt(ctx, scope, at)
	if err != nil {
		return Value{}, err
	}
	if held.live() == 0 {
		return Value{}, ErrNotFound
	}
	value := Value{Metadata: metadataOf(at, held)}
	if !reveal {
		return value, nil
	}
	plaintext, err := s.open(ctx, scope, at, held)
	if err != nil {
		return Value{}, err
	}
	value.Plaintext = plaintext
	return value, nil
}

func (s Store) open(ctx context.Context, scope Scope, at Coordinate, held cell) (string, error) {
	holder, from, holds, err := s.dereference(ctx, scope, at, held)
	if err != nil {
		return "", err
	}
	plaintext, err := s.Sealer.Open(ctx, coordinateOf(from, holds), holder.Sealed)
	if err != nil {
		return "", err
	}
	return string(plaintext), nil
}

func (s Store) dereference(ctx context.Context, scope Scope, at Coordinate, held cell) (cell, Scope, Coordinate, error) {
	if held.Target == nil {
		return held, scope, at, nil
	}
	from := Scope{Project: held.Target.Project, Class: scope.Class}
	holds := Coordinate{Cell: held.Target.Cell}
	_, holder, err := s.cellAt(ctx, from, holds)
	if err != nil {
		return cell{}, Scope{}, Coordinate{}, err
	}
	if holder.live() == 0 {
		return cell{}, Scope{}, Coordinate{}, fmt.Errorf("%s references %s, which holds no value: %w", at, held.Target, ErrDangling)
	}
	if holder.Target != nil {
		return cell{}, Scope{}, Coordinate{}, fmt.Errorf("%s references %s, which is itself a reference: %w", at, held.Target, ErrWouldDeepen)
	}
	return holder, from, holds, nil
}

func (s Store) List(ctx context.Context, scope Scope) ([]Metadata, error) {
	held, err := s.Records.List(ctx, cellsName(scope))
	if err != nil {
		return nil, fmt.Errorf("read %s's values: %w", scope.Project, err)
	}
	out := make([]Metadata, 0, len(held))
	for _, record := range held {
		at, ok := cellOf(record.Name)
		if !ok {
			continue
		}
		var stored cell
		if err := json.Unmarshal(record.Bytes, &stored); err != nil {
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
	holding := make([]cell, 0, len(cells))
	for _, at := range cells {
		held, ok := stored[cellName(scope, at).String()]
		if !ok || held.live() == 0 {
			continue
		}
		found = append(found, at)
		holding = append(holding, held)
	}

	holders, err := s.gather(ctx, scope, stored, holding)
	if err != nil {
		return nil, err
	}

	sealed := make([]cell, len(found))
	from := make([]Scope, len(found))
	holds := make([]Coordinate, len(found))
	for i, at := range found {
		if holding[i].Target == nil {
			sealed[i], from[i], holds[i] = holding[i], scope, at
			continue
		}
		target := *holding[i].Target
		from[i] = Scope{Project: target.Project, Class: scope.Class}
		holds[i] = Coordinate{Cell: target.Cell}
		holder := holders[cellName(from[i], holds[i]).String()]
		if holder.live() == 0 {
			return nil, fmt.Errorf("%s references %s, which holds no value: %w", at, &target, ErrDangling)
		}
		if holder.Target != nil {
			return nil, fmt.Errorf("%s references %s, which is itself a reference: %w", at, &target, ErrWouldDeepen)
		}
		sealed[i] = holder
	}

	opening := make([]int, 0, len(found))
	slotOf := make([]int, len(found))
	slots := make(map[string]int, len(found))
	for i := range found {
		key := cellName(from[i], holds[i]).String()
		slot, seen := slots[key]
		if !seen {
			slot = len(opening)
			slots[key] = slot
			opening = append(opening, i)
		}
		slotOf[i] = slot
	}

	plaintexts := make([]string, len(opening))
	if err := each(ctx, len(opening), func(ctx context.Context, slot int) error {
		i := opening[slot]
		plaintext, err := s.Sealer.Open(ctx, coordinateOf(from[i], holds[i]), sealed[i].Sealed)
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
		out = append(out, Value{Metadata: metadataOf(at, holding[i]), Plaintext: plaintexts[slotOf[i]]})
	}
	return out, nil
}

func (s Store) storedCells(ctx context.Context, scope Scope) (map[string]cell, error) {
	held, err := s.Records.List(ctx, cellsName(scope))
	if err != nil {
		return nil, fmt.Errorf("read %s's values: %w", scope.Project, err)
	}
	out := make(map[string]cell, len(held))
	for _, record := range held {
		var stored cell
		if err := json.Unmarshal(record.Bytes, &stored); err != nil {
			return nil, fmt.Errorf("read %s: %w", record.Name, err)
		}
		out[record.Name.String()] = stored
	}
	return out, nil
}

func (s Store) gather(ctx context.Context, scope Scope, stored map[string]cell, holding []cell) (map[string]cell, error) {
	var from []Scope
	var holds []Coordinate
	wanted := map[string]bool{}
	for _, held := range holding {
		if held.Target == nil {
			continue
		}
		at := Scope{Project: held.Target.Project, Class: scope.Class}
		holder := Coordinate{Cell: held.Target.Cell}
		key := cellName(at, holder).String()
		if wanted[key] {
			continue
		}
		wanted[key] = true
		from = append(from, at)
		holds = append(holds, holder)
	}

	holders := make([]cell, len(from))
	if err := each(ctx, len(from), func(ctx context.Context, i int) error {
		if from[i].Project == scope.Project {
			holders[i] = stored[cellName(from[i], holds[i]).String()]
			return nil
		}
		_, holder, err := s.cellAt(ctx, from[i], holds[i])
		holders[i] = holder
		return err
	}); err != nil {
		return nil, err
	}

	out := make(map[string]cell, len(from))
	for i := range from {
		out[cellName(from[i], holds[i]).String()] = holders[i]
	}
	return out, nil
}

func (s Store) Delete(ctx context.Context, scope Scope, at Coordinate, expected *int64) (bool, error) {
	held, current, err := s.cellAt(ctx, scope, at)
	if err != nil {
		return false, err
	}
	if expected != nil && *expected != current.live() {
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

	tombstone := cell{Version: current.Version, UpdatedAt: s.now(), Deleted: true}
	encoded, err := json.Marshal(tombstone)
	if err != nil {
		return false, fmt.Errorf("encode %s: %w", at, err)
	}
	held.Bytes = encoded
	if _, err := s.Records.Write(ctx, held); err != nil {
		if errors.Is(err, ports.ErrStale) {
			return false, ErrStaleVersion
		}
		return false, fmt.Errorf("delete %s: %w", at.Key, err)
	}
	return true, nil
}

func (s Store) Versions(ctx context.Context, scope Scope, at Coordinate) ([]Version, error) {
	held, err := s.Records.List(ctx, historyName(scope, at))
	if err != nil {
		return nil, fmt.Errorf("read %s's versions: %w", at, err)
	}
	out := make([]Version, 0, len(held))
	for _, record := range held {
		var entry Version
		if err := json.Unmarshal(record.Bytes, &entry); err != nil {
			return nil, fmt.Errorf("read a version of %s: %w", at, err)
		}
		out = append(out, entry)
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

	held, err := s.Records.List(ctx, Under(scope))
	if err != nil {
		return 0, fmt.Errorf("read %s's values: %w", scope.Project, err)
	}
	for _, record := range held {
		if err := ports.Forget(ctx, s.Records, record.Name); err != nil {
			return 0, fmt.Errorf("remove %s's stored values: %w", scope.Project, err)
		}
	}
	refs, err := s.Records.List(ctx, Refs(scope))
	if err != nil {
		return 0, fmt.Errorf("read what references %s: %w", scope.Project, err)
	}
	for _, record := range refs {
		if err := ports.Forget(ctx, s.Records, record.Name); err != nil {
			return 0, fmt.Errorf("remove what references %s: %w", scope.Project, err)
		}
	}
	return len(held), nil
}

func (s Store) cellAt(ctx context.Context, scope Scope, at Coordinate) (ports.Record, cell, error) {
	held, err := ports.Held(ctx, s.Records, cellName(scope, at))
	if err != nil {
		return ports.Record{}, cell{}, fmt.Errorf("read %s: %w", at, err)
	}
	if len(held.Bytes) == 0 {
		return held, cell{}, nil
	}
	var stored cell
	if err := json.Unmarshal(held.Bytes, &stored); err != nil {
		return ports.Record{}, cell{}, fmt.Errorf("read %s: %w", at, err)
	}
	return held, stored, nil
}

func metadataOf(at Coordinate, held cell) Metadata {
	return Metadata{
		Coordinate: at,
		Version:    held.Version,
		UpdatedAt:  held.UpdatedAt,
		Size:       held.Size,
		Target:     held.Target,
	}
}

func coordinateOf(scope Scope, at Coordinate) ports.Coordinate {
	at = at.canonical()
	return ports.Coordinate{
		Project: scope.Project,
		Class:   scope.Class,
		Env:     at.Environment,
		Folder:  at.Folder,
		Name:    at.Key,
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
