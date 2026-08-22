package values

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/pkg/providerkit/ports"
)

type Reference struct {
	Project string
	Coordinate
}

func (r Reference) String() string { return r.Project + "/" + r.Coordinate.String() }

func (s Store) SetReference(ctx context.Context, scope Scope, at Coordinate, target Target) (Metadata, error) {
	if target.Project == "" {
		return Metadata{}, fmt.Errorf("a reference names no project to resolve against")
	}
	holds := Coordinate{Cell: target.Cell}
	to := Scope{Project: target.Project, Class: scope.Class}
	if scope == to && at.canonical() == holds.canonical() {
		return Metadata{}, fmt.Errorf("%s would reference itself: %w", at, ErrWouldDeepen)
	}

	_, pointedAt, err := s.cellAt(ctx, to, holds)
	if err != nil {
		return Metadata{}, err
	}
	if pointedAt.Target != nil {
		return Metadata{}, fmt.Errorf("%s is itself a reference: %w", &target, ErrWouldDeepen)
	}

	consumers, err := s.References(ctx, scope, at)
	if err != nil {
		return Metadata{}, err
	}
	if len(consumers) > 0 {
		return Metadata{}, fmt.Errorf("%s is referenced by %s, which would then be reading a reference: %w", at, describe(consumers), ErrWouldDeepen)
	}

	held, current, err := s.cellAt(ctx, scope, at)
	if err != nil {
		return Metadata{}, err
	}
	if current.Target != nil {
		if err := s.unindexReference(ctx, scope, at, current.Target); err != nil {
			return Metadata{}, err
		}
	}
	metadata, err := s.commit(ctx, scope, at, held, current, nil, cell{Target: &target})
	if err != nil {
		return Metadata{}, err
	}
	if err := s.indexReference(ctx, scope, at, target); err != nil {
		return Metadata{}, err
	}
	return metadata, nil
}

func (s Store) References(ctx context.Context, scope Scope, at Coordinate) ([]Reference, error) {
	if at.Environment != "" && at.Environment != ClassWideEnvironment {
		return nil, nil
	}
	held, err := s.Records.List(ctx, refsName(scope, at))
	if err != nil {
		return nil, fmt.Errorf("read what references %s: %w", at, err)
	}
	out := make([]Reference, 0, len(held))
	for _, record := range held {
		holds, ok := cellOf(record.Name)
		if !ok || len(record.Name) < 4 {
			continue
		}
		out = append(out, Reference{Project: record.Name[len(record.Name)-4], Coordinate: holds})
	}
	slices.SortFunc(out, func(a, b Reference) int { return strings.Compare(a.String(), b.String()) })
	return out, nil
}

func (s Store) ReferenceOwners(ctx context.Context, scope Scope) (map[Coordinate]string, error) {
	held, err := s.List(ctx, scope)
	if err != nil {
		return nil, err
	}
	owners := map[Coordinate]string{}
	for _, m := range held {
		if m.Target != nil && m.Target.Project != scope.Project {
			owners[m.Coordinate] = m.Target.Project
		}
	}
	return owners, nil
}

func (s Store) indexReference(ctx context.Context, scope Scope, at Coordinate, target Target) error {
	to := Scope{Project: target.Project, Class: scope.Class}
	name := refName(to, Coordinate{Cell: target.Cell}, scope, at)
	held, err := ports.Held(ctx, s.Records, name)
	if err != nil {
		return fmt.Errorf("record that %s references %s: %w", at, &target, err)
	}
	held.Bytes = []byte("{}")
	if _, err := s.Records.Write(ctx, held); err != nil {
		return fmt.Errorf("record that %s references %s: %w", at, &target, err)
	}
	return nil
}

func (s Store) unindexReference(ctx context.Context, scope Scope, at Coordinate, target *Target) error {
	to := Scope{Project: target.Project, Class: scope.Class}
	if err := ports.Forget(ctx, s.Records, refName(to, Coordinate{Cell: target.Cell}, scope, at)); err != nil {
		return fmt.Errorf("forget that %s references %s: %w", at, target, err)
	}
	return nil
}

func describe(refs []Reference) string {
	names := make([]string, 0, len(refs))
	for _, r := range refs {
		names = append(names, r.String())
	}
	slices.Sort(names)
	return strings.Join(names, ", ")
}
