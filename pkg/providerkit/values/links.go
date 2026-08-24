package values

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"unicode"

	"github.com/ocelhq/ocel/pkg/providerkit/ports"
)

const (
	OwnerOcel = "OCEL"

	linkValueKey = "PROPERTIES"

	linkAttempts = 5
)

var (
	ErrClaimed = errors.New("values: link claimed by another publisher")

	ErrNotPublished = errors.New("values: link not published")

	ErrTornPair = errors.New("values: torn link pair")
)

type Publishing struct {
	Name string
	Pair Pair
}

type Pair struct {
	Record []byte
	Shapes []byte
	Value  []byte
	Owner  string
}

type Published struct {
	Name        string
	Environment string
	Record      []byte
	Shapes      []byte
	Value       []byte
	Owner       string
	Version     int64
	UpdatedAt   int64
}

type linkRecord struct {
	Version   int64  `json:"version"`
	UpdatedAt int64  `json:"updatedAt"`
	Record    []byte `json:"record"`
	Shapes    []byte `json:"shapes,omitempty"`
	Owner     string `json:"owner,omitempty"`
}

func (r linkRecord) owner() string {
	if r.Owner == "" {
		return OwnerOcel
	}
	return r.Owner
}

type linkValue struct {
	Version int64  `json:"version"`
	Sealed  []byte `json:"sealed"`
}

type ownerIndex struct {
	Names []string `json:"names,omitempty"`
}

func ValidateLinkName(environment, name string) error {
	if err := ValidateLinkEnvironment(environment); err != nil {
		return err
	}
	if name == "" {
		return fmt.Errorf("a link name is required")
	}
	return nil
}

func ValidateLinkEnvironment(environment string) error {
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
				"%s %q carries the control character %q: a coordinate is written into store keys, log lines and generated files, and a character that breaks a line breaks all three",
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

func (s Store) SetLink(ctx context.Context, scope Scope, environment, owner, name string, pair Pair) (int64, error) {
	versions, err := s.SetLinks(ctx, scope, environment, owner, []Publishing{{Name: name, Pair: pair}})
	if err != nil {
		return 0, err
	}
	return versions[0], nil
}

func (s Store) SetLinks(ctx context.Context, scope Scope, environment, owner string, links []Publishing) ([]int64, error) {
	if err := ValidateOwner(owner); err != nil {
		return nil, err
	}
	if err := ValidateLinkEnvironment(environment); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(links))
	for _, link := range links {
		if err := ValidateLinkName(environment, link.Name); err != nil {
			return nil, err
		}
		names = append(names, link.Name)
	}
	if len(links) == 0 {
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

	versions := make([]int64, len(links))
	if err := each(ctx, len(links), func(ctx context.Context, i int) error {
		version, err := s.writePair(ctx, scope, environment, owner, links[i].Name, links[i].Pair)
		versions[i] = version
		return err
	}); err != nil {
		return nil, err
	}
	return versions, nil
}

func (s Store) writePair(ctx context.Context, scope Scope, environment, owner, name string, pair Pair) (int64, error) {
	sealed, err := s.Sealer.Seal(ctx, linkCoordinate(scope, environment, name), pair.Value)
	if err != nil {
		return 0, err
	}

	heldRecord, record, err := s.linkRecordAt(ctx, scope, environment, name)
	if err != nil {
		return 0, err
	}
	if owner != OwnerOcel && record.Version > 0 && record.owner() != owner {
		return 0, s.claimRefusal(scope, name, record.owner(), owner)
	}
	heldValue, err := ports.Held(ctx, s.Records, linkValueName(scope, environment, name))
	if err != nil {
		return 0, fmt.Errorf("read link %s's value: %w", name, err)
	}

	next := record.Version + 1
	written, err := json.Marshal(linkRecord{
		Version:   next,
		UpdatedAt: s.now(),
		Record:    pair.Record,
		Shapes:    pair.Shapes,
		Owner:     owner,
	})
	if err != nil {
		return 0, fmt.Errorf("encode link %s's record: %w", name, err)
	}
	beside, err := json.Marshal(linkValue{Version: next, Sealed: sealed})
	if err != nil {
		return 0, fmt.Errorf("encode link %s's value: %w", name, err)
	}

	heldValue.Bytes = beside
	heldRecord.Bytes = written
	if err := s.Records.WritePair(ctx, heldValue, heldRecord); err != nil {
		return 0, s.racedPair(err, name)
	}
	return next, nil
}

func (s Store) racedPair(err error, name string) error {
	if errors.Is(err, ports.ErrStale) {
		return fmt.Errorf(
			"link %s was rewritten while this publish was writing it: another deploy of the same environment is racing this one — run them one after the other: %w",
			name, ErrTornPair)
	}
	return fmt.Errorf("publish link %s: %w", name, err)
}

func (s Store) claimRefusal(scope Scope, name, by, asking string) error {
	return fmt.Errorf(
		"link %s in %s is already published by %s, and %s is asking to write it: one link name belongs to one publisher, and taking it would hand every app consuming that name another resource's values. "+
			"Give one of them another name, or remove the published one first: %w",
		name, scope.Class, describeOwner(by), describeOwner(asking), ErrClaimed)
}

func describeOwner(owner string) string {
	if owner == OwnerOcel {
		return "ocel's own provisioning"
	}
	return "publisher " + owner
}

func (s Store) RemoveLink(ctx context.Context, scope Scope, environment, name string) (bool, error) {
	removed, err := s.RemoveLinks(ctx, scope, environment, []string{name})
	if err != nil {
		return false, err
	}
	return removed[0], nil
}

func (s Store) RemoveLinks(ctx context.Context, scope Scope, environment string, names []string) ([]bool, error) {
	if err := ValidateLinkEnvironment(environment); err != nil {
		return nil, err
	}
	for _, name := range names {
		if err := ValidateLinkName(environment, name); err != nil {
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
	held := map[string][]string{}
	for i, name := range names {
		for _, c := range claimed[name] {
			if c.environment != at {
				continue
			}
			removed[i] = true
			if !slices.Contains(held[c.owner], name) {
				held[c.owner] = append(held[c.owner], name)
			}
		}
	}

	for _, owner := range slices.Sorted(maps.Keys(held)) {
		if err := s.unclaim(ctx, scope, owner, environment, held[owner]...); err != nil {
			return nil, err
		}
	}

	dropping := make([]string, 0, len(names))
	for i, name := range names {
		if removed[i] && !slices.Contains(dropping, name) {
			dropping = append(dropping, name)
		}
	}
	if err := each(ctx, len(dropping), func(ctx context.Context, i int) error {
		for _, name := range []ports.RecordName{
			linkRecordName(scope, environment, dropping[i]),
			linkValueName(scope, environment, dropping[i]),
		} {
			if err := ports.Forget(ctx, s.Records, name); err != nil {
				return fmt.Errorf("remove %s: %w", name, err)
			}
		}
		return nil
	}); err != nil {
		return nil, err
	}
	return removed, nil
}

func (s Store) ResolveLink(ctx context.Context, scope Scope, environment, name string) (Published, error) {
	resolved, err := s.ResolveLinks(ctx, scope, environment, []string{name})
	if err != nil {
		return Published{}, err
	}
	return resolved[0], nil
}

func (s Store) ResolveLinks(ctx context.Context, scope Scope, environment string, names []string) ([]Published, error) {
	for _, name := range names {
		if err := ValidateLinkName(environment, name); err != nil {
			return nil, err
		}
	}
	if len(names) == 0 {
		return nil, nil
	}

	out := make([]Published, len(names))
	sealed := make([][]byte, len(names))
	for range linkAttempts {
		held := s.pages(scope)
		torn := ""
		for i, name := range names {
			resolved, value, err := s.readPair(ctx, held, scope, environment, name)
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
		if err := each(ctx, len(names), func(ctx context.Context, i int) error {
			plaintext, err := s.Sealer.Open(ctx, linkCoordinate(scope, out[i].Environment, names[i]), sealed[i])
			if err != nil {
				return fmt.Errorf("open link %s's value: %w", names[i], err)
			}
			out[i].Value = plaintext
			return nil
		}); err != nil {
			return nil, err
		}
		return out, nil
	}
	return nil, fmt.Errorf(
		"a link's record and the value beside it came from different publishes, %d reads in a row. "+
			"A deploy is rewriting %s; nothing will be served half of one publish and half of another: %w",
		linkAttempts, describeEnvironment(environment), ErrTornPair)
}

func (s Store) readPair(ctx context.Context, held *pages, scope Scope, environment, name string) (Published, []byte, error) {
	for _, at := range shadowing(environment) {
		published, err := held.at(ctx, at)
		if err != nil {
			return Published{}, nil, err
		}
		record, err := decodeLinkRecord(name, published[linkRecordName(scope, at, name).String()])
		if err != nil {
			return Published{}, nil, err
		}
		value, err := decodeLinkValue(name, published[linkValueName(scope, at, name).String()])
		if err != nil {
			return Published{}, nil, err
		}
		if record.Version == 0 && value.Version == 0 {
			continue
		}
		if record.Version != value.Version {
			return Published{}, nil, ErrTornPair
		}
		return Published{
			Name:        name,
			Environment: at,
			Record:      record.Record,
			Shapes:      record.Shapes,
			Owner:       record.owner(),
			Version:     record.Version,
			UpdatedAt:   record.UpdatedAt,
		}, value.Sealed, nil
	}
	return Published{}, nil, fmt.Errorf("link %s is not published to %s: %w", name, describeEnvironment(environment), ErrNotPublished)
}

func (s Store) ListLinks(ctx context.Context, scope Scope, environment string) ([]Published, error) {
	if err := ValidateLinkEnvironment(environment); err != nil {
		return nil, err
	}
	names, err := s.PublishedNames(ctx, scope, environment)
	if err != nil {
		return nil, err
	}

	held := s.pages(scope)
	out := make([]Published, 0, len(names))
	for _, name := range names {
		for _, at := range shadowing(environment) {
			published, err := held.at(ctx, at)
			if err != nil {
				return nil, err
			}
			record, err := decodeLinkRecord(name, published[linkRecordName(scope, at, name).String()])
			if err != nil {
				return nil, err
			}
			if record.Version == 0 {
				continue
			}
			out = append(out, Published{
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
	slices.SortFunc(out, func(a, b Published) int { return strings.Compare(a.Name, b.Name) })
	return out, nil
}

type pages struct {
	store Store
	scope Scope
	held  map[string]map[string]ports.Record
}

func (s Store) pages(scope Scope) *pages {
	return &pages{store: s, scope: scope, held: map[string]map[string]ports.Record{}}
}

func (p *pages) at(ctx context.Context, environment string) (map[string]ports.Record, error) {
	if held, ok := p.held[environment]; ok {
		return held, nil
	}
	stored, err := p.store.Records.List(ctx, Under(p.scope, "links", escape(environment)))
	if err != nil {
		return nil, fmt.Errorf("read %s's published links: %w", p.scope.Project, err)
	}
	held := make(map[string]ports.Record, len(stored))
	for _, record := range stored {
		held[record.Name.String()] = record
	}
	p.held[environment] = held
	return held, nil
}

func (s Store) PublishedNames(ctx context.Context, scope Scope, environment string) ([]string, error) {
	held, err := s.Records.List(ctx, linkOwnersName(scope))
	if err != nil {
		return nil, fmt.Errorf("read %s's published links: %w", scope.Project, err)
	}
	names := map[string]bool{}
	for _, record := range held {
		at := unescape(record.Name[len(record.Name)-1])
		if !bindsTo(at, environment) {
			continue
		}
		var index ownerIndex
		if err := json.Unmarshal(record.Bytes, &index); err != nil {
			return nil, fmt.Errorf("read %s's published links: %w", scope.Project, err)
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
	held, err := s.Records.List(ctx, linkOwnersName(scope))
	if err != nil {
		return nil, fmt.Errorf("read %s's published links: %w", scope.Project, err)
	}
	out := map[string][]claim{}
	for _, record := range held {
		if len(record.Name) < 2 {
			continue
		}
		owner := unescape(record.Name[len(record.Name)-2])
		at := unescape(record.Name[len(record.Name)-1])
		var index ownerIndex
		if err := json.Unmarshal(record.Bytes, &index); err != nil {
			return nil, fmt.Errorf("read %s's published links: %w", scope.Project, err)
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
		return slices.DeleteFunc(slices.Clone(names), func(held string) bool { return slices.Contains(dropping, held) })
	})
}

func (s Store) reindex(ctx context.Context, scope Scope, owner, environment string, apply func([]string) []string) error {
	at := linkOwnerName(scope, owner, environment)
	for range linkAttempts {
		held, err := ports.Held(ctx, s.Records, at)
		if err != nil {
			return fmt.Errorf("read %s's published links: %w", owner, err)
		}
		var index ownerIndex
		if len(held.Bytes) > 0 {
			if err := json.Unmarshal(held.Bytes, &index); err != nil {
				return fmt.Errorf("read %s's published links: %w", owner, err)
			}
		}
		kept := apply(index.Names)
		if slices.Equal(kept, index.Names) {
			return nil
		}
		slices.Sort(kept)

		if len(kept) == 0 {
			if err := s.Records.Remove(ctx, at, held.Revision); err != nil {
				if errors.Is(err, ports.ErrStale) {
					continue
				}
				if !errors.Is(err, ports.ErrNoRecord) {
					return fmt.Errorf("record %s's published links: %w", owner, err)
				}
			}
			return nil
		}
		encoded, err := json.Marshal(ownerIndex{Names: kept})
		if err != nil {
			return fmt.Errorf("encode %s's published links: %w", owner, err)
		}
		held.Bytes = encoded
		if _, err := s.Records.Write(ctx, held); err != nil {
			if errors.Is(err, ports.ErrStale) {
				continue
			}
			return fmt.Errorf("record %s's published links: %w", owner, err)
		}
		return nil
	}
	return fmt.Errorf(
		"another deploy of %s kept rewriting its published links while this one tried to record its own, %d times over. "+
			"Two deploys of the same environment are racing; run them one after the other",
		scope.Project, linkAttempts)
}

func (s Store) linkRecordAt(ctx context.Context, scope Scope, environment, name string) (ports.Record, linkRecord, error) {
	held, err := ports.Held(ctx, s.Records, linkRecordName(scope, environment, name))
	if err != nil {
		return ports.Record{}, linkRecord{}, fmt.Errorf("read link %s's record: %w", name, err)
	}
	record, err := decodeLinkRecord(name, held)
	if err != nil {
		return ports.Record{}, linkRecord{}, err
	}
	return held, record, nil
}

func decodeLinkRecord(name string, held ports.Record) (linkRecord, error) {
	if len(held.Bytes) == 0 {
		return linkRecord{}, nil
	}
	var record linkRecord
	if err := json.Unmarshal(held.Bytes, &record); err != nil {
		return linkRecord{}, fmt.Errorf("read link %s's record: %w", name, err)
	}
	return record, nil
}

func decodeLinkValue(name string, held ports.Record) (linkValue, error) {
	if len(held.Bytes) == 0 {
		return linkValue{}, nil
	}
	var value linkValue
	if err := json.Unmarshal(held.Bytes, &value); err != nil {
		return linkValue{}, fmt.Errorf("read link %s's value: %w", name, err)
	}
	return value, nil
}

func linkCoordinate(scope Scope, environment, name string) ports.Coordinate {
	return ports.Coordinate{
		Project: scope.Project,
		Class:   scope.Class,
		Env:     canonicalEnvironment(environment),
		Folder:  rootFolder,
		Link:    name,
		Name:    linkValueKey,
	}
}
