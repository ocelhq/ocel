package variablestore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"unicode"

	"github.com/ocelhq/ocel/pkg/keyvalue"
)

const (
	OwnerOcel = "OCEL"

	bindingValueKey = "PROPERTIES"

	bindingAttempts = 5
)

var (
	ErrClaimed = errors.New("variablestore: binding claimed by another publisher")

	ErrNotPublished = errors.New("variablestore: binding not published")

	ErrTornPair = errors.New("variablestore: torn binding pair")

	ErrBindingChanged = errors.New("variablestore: binding changed since its version was read")
)

type NamedBindingWrite struct {
	Name     string
	Write    BindingWrite
	Expected *int64
}

type BindingWrite struct {
	Record []byte
	Shapes []byte
	Value  []byte
	Owner  string
}

type StoredBinding struct {
	Name        string
	Environment string
	Record      []byte
	Shapes      []byte
	Value       []byte
	Owner       string
	Version     int64
	UpdatedAt   int64
}

type bindingRecord struct {
	Version   int64  `json:"version"`
	UpdatedAt int64  `json:"updatedAt"`
	Record    []byte `json:"record"`
	Shapes    []byte `json:"shapes,omitempty"`
	Owner     string `json:"owner,omitempty"`
}

func (r bindingRecord) owner() string {
	if r.Owner == "" {
		return OwnerOcel
	}
	return r.Owner
}

type bindingValue struct {
	Version int64  `json:"version"`
	Sealed  []byte `json:"sealed"`
}

type ownerIndex struct {
	Names []string `json:"names,omitempty"`
}

func ValidateBindingName(environment, name string) error {
	if err := ValidateBindingEnvironment(environment); err != nil {
		return err
	}
	if name == "" {
		return fmt.Errorf("a binding name is required")
	}
	return nil
}

func ValidateBindingEnvironment(environment string) error {
	if environment == TierWideEnvironment {
		return fmt.Errorf(
			"%q is reserved: it names the pair that binds tier-wide. Leave the environment off to publish there, which serves every preview including the ephemeral ones",
			TierWideEnvironment)
	}
	return refuseControl("environment name", environment)
}

func ValidateProject(project string) error {
	if project == "" {
		return fmt.Errorf("a project slug is required")
	}
	return refuseControl("project slug", project)
}

func refuseControl(what, value string) error {
	for _, r := range value {
		if unicode.IsControl(r) {
			return fmt.Errorf(
				"%s %q contains the control character %q: a coordinate is written into store keys, log lines and generated files, and a character that breaks a line breaks all three",
				what, value, r)
		}
	}
	return nil
}

func ValidateOwner(owner string) error {
	if owner == "" {
		return fmt.Errorf("a publisher name is required: it is what keeps one publisher from pruning another's records")
	}
	return nil
}

func (s Store) SetBinding(ctx context.Context, scope Scope, environment, owner, name string, pair BindingWrite) (int64, error) {
	versions, err := s.SetBindings(ctx, scope, environment, owner, []NamedBindingWrite{{Name: name, Write: pair}})
	if err != nil {
		return 0, err
	}
	return versions[0], nil
}

func (s Store) SetBindings(ctx context.Context, scope Scope, environment, owner string, bindings []NamedBindingWrite) ([]int64, error) {
	if err := ValidateOwner(owner); err != nil {
		return nil, err
	}
	if err := ValidateBindingEnvironment(environment); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(bindings))
	for _, binding := range bindings {
		if err := ValidateBindingName(environment, binding.Name); err != nil {
			return nil, err
		}
		names = append(names, binding.Name)
	}
	if len(bindings) == 0 {
		return nil, nil
	}

	claimed, err := s.claims(ctx, scope)
	if err != nil {
		return nil, err
	}
	for _, name := range names {
		if by, taken := otherOwner(claimed[name], owner); taken {
			return nil, s.claimRefusal(scope, name, by, owner)
		}
	}
	if err := s.claim(ctx, scope, owner, environment, names...); err != nil {
		return nil, err
	}

	versions := make([]int64, len(bindings))
	if err := forEachConcurrently(ctx, len(bindings), func(ctx context.Context, i int) error {
		version, err := s.writePair(ctx, scope, environment, owner, bindings[i])
		versions[i] = version
		return err
	}); err != nil {
		return nil, err
	}
	return versions, nil
}

func (s Store) writePair(ctx context.Context, scope Scope, environment, owner string, write NamedBindingWrite) (int64, error) {
	name, pair := write.Name, write.Write
	bound, err := newBindingAssociatedData(scope, environment, name)
	if err != nil {
		return 0, err
	}
	sealed, err := s.Cipher.Seal(ctx, scope.Tier, bound, pair.Value)
	if err != nil {
		return 0, err
	}

	recordedBinding, record, err := s.bindingRecordAt(ctx, scope, environment, name)
	if err != nil {
		return 0, err
	}
	if owner != OwnerOcel && record.Version > 0 && record.owner() != owner {
		return 0, s.claimRefusal(scope, name, record.owner(), owner)
	}
	if write.Expected != nil && record.Version != *write.Expected {
		return 0, changedRefusal(name, *write.Expected, record.Version)
	}
	recordedValue, err := keyvalue.ReadOrEmpty(ctx, s.KeyValues, bindingValueAt(scope, name, environment))
	if err != nil {
		return 0, fmt.Errorf("read binding %s's value: %w", name, err)
	}

	next := record.Version + 1
	written, err := json.Marshal(bindingRecord{
		Version:   next,
		UpdatedAt: s.now(),
		Record:    pair.Record,
		Shapes:    pair.Shapes,
		Owner:     owner,
	})
	if err != nil {
		return 0, fmt.Errorf("encode binding %s's record: %w", name, err)
	}
	beside, err := json.Marshal(bindingValue{Version: next, Sealed: sealed})
	if err != nil {
		return 0, fmt.Errorf("encode binding %s's value: %w", name, err)
	}

	recordedValue.Value = beside
	recordedBinding.Value = written
	if err := s.KeyValues.WritePair(ctx, recordedValue, recordedBinding); err != nil {
		return 0, s.racedPair(err, name)
	}
	return next, nil
}

func (s Store) racedPair(err error, name string) error {
	if errors.Is(err, keyvalue.ErrStale) {
		return fmt.Errorf(
			"binding %s was rewritten while this publish was writing it: another deploy of the same environment is racing this one — run them one after the other: %w",
			name, ErrTornPair)
	}
	return fmt.Errorf("publish binding %s: %w", name, err)
}

func changedRefusal(name string, expected, recorded int64) error {
	return fmt.Errorf(
		"binding %s is at version %d, and this write was made against version %d: another deploy of the same environment published it in between. Run the deploys one after the other: %w",
		name, recorded, expected, ErrBindingChanged)
}

func (s Store) claimRefusal(scope Scope, name, by, asking string) error {
	return fmt.Errorf(
		"binding %s in %s is already published by %s, and %s is asking to write it: one binding name belongs to one publisher, and taking it would hand every app consuming that name another resource's values. "+
			"Give one of them another name, or remove the published one first: %w",
		name, scope.Tier, describeOwner(by), describeOwner(asking), ErrClaimed)
}

func describeOwner(owner string) string {
	if owner == OwnerOcel {
		return "ocel's own provisioning"
	}
	return "publisher " + owner
}

func (s Store) RemoveBinding(ctx context.Context, scope Scope, environment, name string) (bool, error) {
	removed, err := s.RemoveBindings(ctx, scope, environment, []string{name})
	if err != nil {
		return false, err
	}
	return removed[0], nil
}

func (s Store) RemoveBindings(ctx context.Context, scope Scope, environment string, names []string) ([]bool, error) {
	if err := ValidateBindingEnvironment(environment); err != nil {
		return nil, err
	}
	for _, name := range names {
		if err := ValidateBindingName(environment, name); err != nil {
			return nil, err
		}
	}
	if len(names) == 0 {
		return nil, nil
	}

	claimed, err := s.claims(ctx, scope)
	if err != nil {
		return nil, err
	}
	at := canonicalEnvironment(environment)
	removed := make([]bool, len(names))
	remaining := map[string][]string{}
	for i, name := range names {
		for _, c := range claimed[name] {
			if c.environment != at {
				continue
			}
			removed[i] = true
			if !slices.Contains(remaining[c.owner], name) {
				remaining[c.owner] = append(remaining[c.owner], name)
			}
		}
	}

	for _, owner := range slices.Sorted(maps.Keys(remaining)) {
		if err := s.unclaim(ctx, scope, owner, environment, remaining[owner]...); err != nil {
			return nil, err
		}
	}

	dropping := make([]string, 0, len(names))
	for i, name := range names {
		if removed[i] && !slices.Contains(dropping, name) {
			dropping = append(dropping, name)
		}
	}
	if err := forEachConcurrently(ctx, len(dropping), func(ctx context.Context, i int) error {
		for _, name := range []keyvalue.Key{
			bindingRecordAt(scope, dropping[i], environment),
			bindingValueAt(scope, dropping[i], environment),
		} {
			if err := keyvalue.Forget(ctx, s.KeyValues, name); err != nil {
				return fmt.Errorf("remove %s: %w", name, err)
			}
		}
		return nil
	}); err != nil {
		return nil, err
	}
	return removed, nil
}

func (s Store) RemoveUnchangedBindings(ctx context.Context, scope Scope, environment string, expected map[string]int64) ([]string, error) {
	if err := ValidateBindingEnvironment(environment); err != nil {
		return nil, err
	}
	names := slices.Sorted(maps.Keys(expected))
	for _, name := range names {
		if err := ValidateBindingName(environment, name); err != nil {
			return nil, err
		}
	}
	removed := make([]bool, len(names))
	if err := forEachConcurrently(ctx, len(names), func(ctx context.Context, i int) error {
		var err error
		removed[i], err = s.removeUnchanged(ctx, scope, environment, names[i], expected[names[i]])
		return err
	}); err != nil {
		return nil, err
	}
	var out []string
	for i, name := range names {
		if removed[i] {
			out = append(out, name)
		}
	}
	return out, nil
}

func (s Store) removeUnchanged(ctx context.Context, scope Scope, environment, name string, expected int64) (bool, error) {
	recorded, record, err := s.bindingRecordAt(ctx, scope, environment, name)
	if err != nil {
		return false, err
	}
	if record.Version == 0 || record.Version != expected {
		return false, nil
	}
	recordedValue, err := keyvalue.ReadOrEmpty(ctx, s.KeyValues, bindingValueAt(scope, name, environment))
	if err != nil {
		return false, fmt.Errorf("read binding %s's value: %w", name, err)
	}
	owner := record.owner()
	if err := s.unclaim(ctx, scope, owner, environment, name); err != nil {
		return false, err
	}
	err = s.KeyValues.Remove(ctx, recorded.Key, recorded.Revision)
	if errors.Is(err, keyvalue.ErrStale) {
		return false, s.claim(ctx, scope, owner, environment, name)
	}
	if err != nil && !errors.Is(err, keyvalue.ErrNotFound) {
		return false, fmt.Errorf("remove %s: %w", recorded.Key, err)
	}
	if recordedValue.Revision == "" {
		return true, nil
	}
	err = s.KeyValues.Remove(ctx, recordedValue.Key, recordedValue.Revision)
	if err != nil && !errors.Is(err, keyvalue.ErrStale) && !errors.Is(err, keyvalue.ErrNotFound) {
		return false, fmt.Errorf("remove %s: %w", recordedValue.Key, err)
	}
	return true, nil
}

func (s Store) ResolveBinding(ctx context.Context, scope Scope, environment, name string) (StoredBinding, error) {
	resolved, err := s.ResolveBindings(ctx, scope, environment, []string{name})
	if err != nil {
		return StoredBinding{}, err
	}
	return resolved[0], nil
}

func (s Store) ResolveBindings(ctx context.Context, scope Scope, environment string, names []string) ([]StoredBinding, error) {
	for _, name := range names {
		if err := ValidateBindingName(environment, name); err != nil {
			return nil, err
		}
	}
	if len(names) == 0 {
		return nil, nil
	}

	out := make([]StoredBinding, len(names))
	sealed := make([][]byte, len(names))
	for range bindingAttempts {
		cache := s.newRecordCache(scope)
		if err := cache.named(ctx, names); err != nil {
			return nil, err
		}
		torn := ""
		for i, name := range names {
			resolved, value, err := s.readPair(cache, scope, environment, name)
			if errors.Is(err, ErrTornPair) {
				torn = name
				break
			}
			if err != nil {
				return nil, err
			}
			out[i], sealed[i] = resolved, value
		}
		if torn != "" {
			continue
		}
		if err := forEachConcurrently(ctx, len(names), func(ctx context.Context, i int) error {
			bound, err := newBindingAssociatedData(scope, out[i].Environment, names[i])
			if err != nil {
				return err
			}
			plaintext, err := s.Cipher.Open(ctx, scope.Tier, bound, sealed[i])
			if err != nil {
				return fmt.Errorf("open binding %s's value: %w", names[i], err)
			}
			out[i].Value = plaintext
			return nil
		}); err != nil {
			return nil, err
		}
		return out, nil
	}
	return nil, fmt.Errorf(
		"a binding's record and the value beside it came from different publishes, %d reads in a row. "+
			"A deploy is rewriting %s; nothing will be served half of one publish and half of another: %w",
		bindingAttempts, describeEnvironment(environment), ErrTornPair)
}

func (s Store) readPair(cache *recordCache, scope Scope, environment, name string) (StoredBinding, []byte, error) {
	for _, at := range shadowing(environment) {
		record, err := decodeBindingRecord(name, cache.at(bindingRecordAt(scope, name, at)))
		if err != nil {
			return StoredBinding{}, nil, err
		}
		value, err := decodeBindingValue(name, cache.at(bindingValueAt(scope, name, at)))
		if err != nil {
			return StoredBinding{}, nil, err
		}
		if record.Version == 0 && value.Version == 0 {
			continue
		}
		if record.Version != value.Version {
			return StoredBinding{}, nil, ErrTornPair
		}
		return StoredBinding{
			Name:        name,
			Environment: at,
			Record:      record.Record,
			Shapes:      record.Shapes,
			Owner:       record.owner(),
			Version:     record.Version,
			UpdatedAt:   record.UpdatedAt,
		}, value.Sealed, nil
	}
	return StoredBinding{}, nil, fmt.Errorf("binding %s is not published to %s: %w", name, describeEnvironment(environment), ErrNotPublished)
}

func (s Store) ListBindings(ctx context.Context, scope Scope, environment string) ([]StoredBinding, error) {
	if err := ValidateBindingEnvironment(environment); err != nil {
		return nil, err
	}
	names, err := s.PublishedNames(ctx, scope, environment)
	if err != nil {
		return nil, err
	}

	cache := s.newRecordCache(scope)
	if err := cache.all(ctx); err != nil {
		return nil, err
	}
	out := make([]StoredBinding, 0, len(names))
	for _, name := range names {
		for _, at := range shadowing(environment) {
			record, err := decodeBindingRecord(name, cache.at(bindingRecordAt(scope, name, at)))
			if err != nil {
				return nil, err
			}
			if record.Version == 0 {
				continue
			}
			out = append(out, StoredBinding{
				Name:        name,
				Environment: at,
				Record:      record.Record,
				Shapes:      record.Shapes,
				Owner:       record.owner(),
				Version:     record.Version,
				UpdatedAt:   record.UpdatedAt,
			})
			break
		}
	}
	slices.SortFunc(out, func(a, b StoredBinding) int { return strings.Compare(a.Name, b.Name) })
	return out, nil
}

type recordCache struct {
	store  Store
	scope  Scope
	byName map[string]keyvalue.Entry
}

func (s Store) newRecordCache(scope Scope) *recordCache {
	return &recordCache{store: s, scope: scope, byName: map[string]keyvalue.Entry{}}
}

func (p *recordCache) named(ctx context.Context, names []string) error {
	if len(names) == 1 {
		return p.load(ctx, bindingPrefix(p.scope, names[0]))
	}
	return p.all(ctx)
}

func (p *recordCache) all(ctx context.Context) error {
	return p.load(ctx, bindingsPrefix(p.scope))
}

func (p *recordCache) load(ctx context.Context, under keyvalue.Key) error {
	stored, err := p.store.listUnder(ctx, under)
	if err != nil {
		return fmt.Errorf("read %s's published bindings: %w", p.scope.Project, err)
	}
	for _, entry := range stored {
		p.byName[entry.Key.String()] = entry
	}
	return nil
}

func (p *recordCache) at(name keyvalue.Key) keyvalue.Entry { return p.byName[name.String()] }

func (s Store) PublishedNames(ctx context.Context, scope Scope, environment string) ([]string, error) {
	recorded, err := s.listUnder(ctx, bindingOwnersPrefix(scope))
	if err != nil {
		return nil, fmt.Errorf("read %s's published bindings: %w", scope.Project, err)
	}
	names := map[string]bool{}
	for _, entry := range recorded {
		at := entry.Key.Path[len(entry.Key.Path)-1]
		if !bindsTo(at, environment) {
			continue
		}
		var index ownerIndex
		if err := json.Unmarshal(entry.Value, &index); err != nil {
			return nil, fmt.Errorf("read %s's published bindings: %w", scope.Project, err)
		}
		for _, name := range index.Names {
			names[name] = true
		}
	}
	return slices.Sorted(maps.Keys(names)), nil
}

func bindsTo(at, environment string) bool {
	return at == canonicalEnvironment(environment) || at == TierWideEnvironment
}

func shadowing(environment string) []string {
	if environment == "" || environment == TierWideEnvironment {
		return []string{""}
	}
	return []string{environment, ""}
}

func describeEnvironment(environment string) string {
	if environment == "" {
		return "the tier"
	}
	return environment
}

type claim struct {
	owner       string
	environment string
}

func (s Store) claims(ctx context.Context, scope Scope) (map[string][]claim, error) {
	recorded, err := s.listUnder(ctx, bindingOwnersPrefix(scope))
	if err != nil {
		return nil, fmt.Errorf("read %s's published bindings: %w", scope.Project, err)
	}
	out := map[string][]claim{}
	for _, entry := range recorded {
		rest, named := entry.Key.Under(bindingOwnersPrefix(scope).Path...)
		if !named || len(rest) != 2 {
			continue
		}
		owner, at := rest[0], rest[1]
		var index ownerIndex
		if err := json.Unmarshal(entry.Value, &index); err != nil {
			return nil, fmt.Errorf("read %s's published bindings: %w", scope.Project, err)
		}
		for _, name := range index.Names {
			out[name] = append(out[name], claim{owner: owner, environment: at})
		}
	}
	return out, nil
}

func otherOwner(claims []claim, owner string) (string, bool) {
	others := make([]string, 0, len(claims))
	for _, c := range claims {
		if c.owner != owner {
			others = append(others, c.owner)
		}
	}
	if len(others) == 0 {
		return "", false
	}
	if slices.Contains(others, OwnerOcel) {
		return OwnerOcel, true
	}
	slices.Sort(others)
	return others[0], true
}

func (s Store) claim(ctx context.Context, scope Scope, owner, environment string, taking ...string) error {
	return s.reindex(ctx, scope, owner, environment, func(names []string) []string {
		kept := slices.Clone(names)
		for _, name := range taking {
			if !slices.Contains(kept, name) {
				kept = append(kept, name)
			}
		}
		return kept
	})
}

func (s Store) unclaim(ctx context.Context, scope Scope, owner, environment string, dropping ...string) error {
	return s.reindex(ctx, scope, owner, environment, func(names []string) []string {
		return slices.DeleteFunc(slices.Clone(names), func(name string) bool { return slices.Contains(dropping, name) })
	})
}

func (s Store) reindex(ctx context.Context, scope Scope, owner, environment string, apply func([]string) []string) error {
	at := bindingOwnerKey(scope, owner, environment)
	for range bindingAttempts {
		recorded, err := keyvalue.ReadOrEmpty(ctx, s.KeyValues, at)
		if err != nil {
			return fmt.Errorf("read %s's published bindings: %w", owner, err)
		}
		var index ownerIndex
		if len(recorded.Value) > 0 {
			if err := json.Unmarshal(recorded.Value, &index); err != nil {
				return fmt.Errorf("read %s's published bindings: %w", owner, err)
			}
		}
		kept := apply(index.Names)
		if slices.Equal(kept, index.Names) {
			return nil
		}
		slices.Sort(kept)

		if len(kept) == 0 {
			if err := s.KeyValues.Remove(ctx, at, recorded.Revision); err != nil {
				if errors.Is(err, keyvalue.ErrStale) {
					continue
				}
				if !errors.Is(err, keyvalue.ErrNotFound) {
					return fmt.Errorf("record %s's published bindings: %w", owner, err)
				}
			}
			return nil
		}
		encoded, err := json.Marshal(ownerIndex{Names: kept})
		if err != nil {
			return fmt.Errorf("encode %s's published bindings: %w", owner, err)
		}
		recorded.Value = encoded
		if _, err := s.KeyValues.Write(ctx, recorded); err != nil {
			if errors.Is(err, keyvalue.ErrStale) {
				continue
			}
			return fmt.Errorf("record %s's published bindings: %w", owner, err)
		}
		return nil
	}
	return fmt.Errorf(
		"another deploy of %s kept rewriting its published bindings while this one tried to record its own, %d times over. "+
			"Two deploys of the same environment are racing; run them one after the other",
		scope.Project, bindingAttempts)
}

func (s Store) bindingRecordAt(ctx context.Context, scope Scope, environment, name string) (keyvalue.Entry, bindingRecord, error) {
	recorded, err := keyvalue.ReadOrEmpty(ctx, s.KeyValues, bindingRecordAt(scope, name, environment))
	if err != nil {
		return keyvalue.Entry{}, bindingRecord{}, fmt.Errorf("read binding %s's record: %w", name, err)
	}
	record, err := decodeBindingRecord(name, recorded)
	if err != nil {
		return keyvalue.Entry{}, bindingRecord{}, err
	}
	return recorded, record, nil
}

func decodeBindingRecord(name string, recorded keyvalue.Entry) (bindingRecord, error) {
	if len(recorded.Value) == 0 {
		return bindingRecord{}, nil
	}
	var record bindingRecord
	if err := json.Unmarshal(recorded.Value, &record); err != nil {
		return bindingRecord{}, fmt.Errorf("read binding %s's record: %w", name, err)
	}
	return record, nil
}

func decodeBindingValue(name string, recorded keyvalue.Entry) (bindingValue, error) {
	if len(recorded.Value) == 0 {
		return bindingValue{}, nil
	}
	var value bindingValue
	if err := json.Unmarshal(recorded.Value, &value); err != nil {
		return bindingValue{}, fmt.Errorf("read binding %s's value: %w", name, err)
	}
	return value, nil
}
