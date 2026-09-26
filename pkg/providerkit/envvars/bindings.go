package envvars

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"unicode"

	"github.com/ocelhq/ocel/pkg/providerkit/records"
)

const (
	OwnerOcel = "OCEL"

	bindingValueKey = "PROPERTIES"

	bindingAttempts = 5
)

var (
	ErrClaimed = errors.New("envvars: binding claimed by another publisher")

	ErrNotPublished = errors.New("envvars: binding not published")

	ErrTornPair = errors.New("envvars: torn binding pair")
)

type NamedBindingWrite struct {
	Name  string
	Write BindingWrite
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
	if environment == ClassWideEnvironment {
		return fmt.Errorf(
			"%q is reserved: it names the pair that binds class-wide. Leave the environment off to publish there, which serves every preview including the ephemeral ones",
			ClassWideEnvironment)
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
		version, err := s.writePair(ctx, scope, environment, owner, bindings[i].Name, bindings[i].Write)
		versions[i] = version
		return err
	}); err != nil {
		return nil, err
	}
	return versions, nil
}

func (s Store) writePair(ctx context.Context, scope Scope, environment, owner, name string, pair BindingWrite) (int64, error) {
	sealed, err := s.Cipher.Seal(ctx, bindingCoordinate(scope, environment, name), pair.Value)
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
	recordedValue, err := records.ReadOrEmpty(ctx, s.Records, bindingValueName(scope, name, environment))
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

	recordedValue.Bytes = beside
	recordedBinding.Bytes = written
	if err := s.Records.WritePair(ctx, recordedValue, recordedBinding); err != nil {
		return 0, s.racedPair(err, name)
	}
	return next, nil
}

func (s Store) racedPair(err error, name string) error {
	if errors.Is(err, records.ErrStale) {
		return fmt.Errorf(
			"binding %s was rewritten while this publish was writing it: another deploy of the same environment is racing this one — run them one after the other: %w",
			name, ErrTornPair)
	}
	return fmt.Errorf("publish binding %s: %w", name, err)
}

func (s Store) claimRefusal(scope Scope, name, by, asking string) error {
	return fmt.Errorf(
		"binding %s in %s is already published by %s, and %s is asking to write it: one binding name belongs to one publisher, and taking it would hand every app consuming that name another resource's values. "+
			"Give one of them another name, or remove the published one first: %w",
		name, scope.Class, describeOwner(by), describeOwner(asking), ErrClaimed)
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
		for _, name := range []records.Name{
			bindingRecordName(scope, dropping[i], environment),
			bindingValueName(scope, dropping[i], environment),
		} {
			if err := records.Forget(ctx, s.Records, name); err != nil {
				return fmt.Errorf("remove %s: %w", name, err)
			}
		}
		return nil
	}); err != nil {
		return nil, err
	}
	return removed, nil
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
			plaintext, err := s.Cipher.Open(ctx, bindingCoordinate(scope, out[i].Environment, names[i]), sealed[i])
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
		record, err := decodeBindingRecord(name, cache.at(bindingRecordName(scope, name, at)))
		if err != nil {
			return StoredBinding{}, nil, err
		}
		value, err := decodeBindingValue(name, cache.at(bindingValueName(scope, name, at)))
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
			record, err := decodeBindingRecord(name, cache.at(bindingRecordName(scope, name, at)))
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
	byName map[string]records.Record
}

func (s Store) newRecordCache(scope Scope) *recordCache {
	return &recordCache{store: s, scope: scope, byName: map[string]records.Record{}}
}

func (p *recordCache) named(ctx context.Context, names []string) error {
	if len(names) == 1 {
		return p.load(ctx, bindingName(p.scope, names[0]))
	}
	return p.all(ctx)
}

func (p *recordCache) all(ctx context.Context) error {
	return p.load(ctx, bindingsName(p.scope))
}

func (p *recordCache) load(ctx context.Context, under records.Name) error {
	stored, err := p.store.Records.List(ctx, under)
	if err != nil {
		return fmt.Errorf("read %s's published bindings: %w", p.scope.Project, err)
	}
	for _, record := range stored {
		p.byName[record.Name.String()] = record
	}
	return nil
}

func (p *recordCache) at(name records.Name) records.Record { return p.byName[name.String()] }

func (s Store) PublishedNames(ctx context.Context, scope Scope, environment string) ([]string, error) {
	recorded, err := s.Records.List(ctx, bindingOwnersName(scope))
	if err != nil {
		return nil, fmt.Errorf("read %s's published bindings: %w", scope.Project, err)
	}
	names := map[string]bool{}
	for _, record := range recorded {
		at := records.Unescape(record.Name[len(record.Name)-1])
		if !bindsTo(at, environment) {
			continue
		}
		var index ownerIndex
		if err := json.Unmarshal(record.Bytes, &index); err != nil {
			return nil, fmt.Errorf("read %s's published bindings: %w", scope.Project, err)
		}
		for _, name := range index.Names {
			names[name] = true
		}
	}
	return slices.Sorted(maps.Keys(names)), nil
}

func bindsTo(at, environment string) bool {
	return at == canonicalEnvironment(environment) || at == ClassWideEnvironment
}

func shadowing(environment string) []string {
	if environment == "" || environment == ClassWideEnvironment {
		return []string{""}
	}
	return []string{environment, ""}
}

func describeEnvironment(environment string) string {
	if environment == "" {
		return "the class"
	}
	return environment
}

type claim struct {
	owner       string
	environment string
}

func (s Store) claims(ctx context.Context, scope Scope) (map[string][]claim, error) {
	recorded, err := s.Records.List(ctx, bindingOwnersName(scope))
	if err != nil {
		return nil, fmt.Errorf("read %s's published bindings: %w", scope.Project, err)
	}
	out := map[string][]claim{}
	for _, record := range recorded {
		if len(record.Name) < 2 {
			continue
		}
		owner := records.Unescape(record.Name[len(record.Name)-2])
		at := records.Unescape(record.Name[len(record.Name)-1])
		var index ownerIndex
		if err := json.Unmarshal(record.Bytes, &index); err != nil {
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
	at := bindingOwnerName(scope, owner, environment)
	for range bindingAttempts {
		recorded, err := records.ReadOrEmpty(ctx, s.Records, at)
		if err != nil {
			return fmt.Errorf("read %s's published bindings: %w", owner, err)
		}
		var index ownerIndex
		if len(recorded.Bytes) > 0 {
			if err := json.Unmarshal(recorded.Bytes, &index); err != nil {
				return fmt.Errorf("read %s's published bindings: %w", owner, err)
			}
		}
		kept := apply(index.Names)
		if slices.Equal(kept, index.Names) {
			return nil
		}
		slices.Sort(kept)

		if len(kept) == 0 {
			if err := s.Records.Remove(ctx, at, recorded.Revision); err != nil {
				if errors.Is(err, records.ErrStale) {
					continue
				}
				if !errors.Is(err, records.ErrNotFound) {
					return fmt.Errorf("record %s's published bindings: %w", owner, err)
				}
			}
			return nil
		}
		encoded, err := json.Marshal(ownerIndex{Names: kept})
		if err != nil {
			return fmt.Errorf("encode %s's published bindings: %w", owner, err)
		}
		recorded.Bytes = encoded
		if _, err := s.Records.Write(ctx, recorded); err != nil {
			if errors.Is(err, records.ErrStale) {
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

func (s Store) bindingRecordAt(ctx context.Context, scope Scope, environment, name string) (records.Record, bindingRecord, error) {
	recorded, err := records.ReadOrEmpty(ctx, s.Records, bindingRecordName(scope, name, environment))
	if err != nil {
		return records.Record{}, bindingRecord{}, fmt.Errorf("read binding %s's record: %w", name, err)
	}
	record, err := decodeBindingRecord(name, recorded)
	if err != nil {
		return records.Record{}, bindingRecord{}, err
	}
	return recorded, record, nil
}

func decodeBindingRecord(name string, recorded records.Record) (bindingRecord, error) {
	if len(recorded.Bytes) == 0 {
		return bindingRecord{}, nil
	}
	var record bindingRecord
	if err := json.Unmarshal(recorded.Bytes, &record); err != nil {
		return bindingRecord{}, fmt.Errorf("read binding %s's record: %w", name, err)
	}
	return record, nil
}

func decodeBindingValue(name string, recorded records.Record) (bindingValue, error) {
	if len(recorded.Bytes) == 0 {
		return bindingValue{}, nil
	}
	var value bindingValue
	if err := json.Unmarshal(recorded.Bytes, &value); err != nil {
		return bindingValue{}, fmt.Errorf("read binding %s's value: %w", name, err)
	}
	return value, nil
}

func bindingCoordinate(scope Scope, environment, name string) records.SealScope {
	return records.SealScope{
		Project: scope.Project,
		Class:   scope.Class,
		Env:     canonicalEnvironment(environment),
		Folder:  rootFolder,
		Binding: name,
		Name:    bindingValueKey,
	}
}
