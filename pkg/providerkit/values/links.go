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
	for _, r := range environment {
		if unicode.IsControl(r) {
			return fmt.Errorf(
				"environment name %q carries the control character %q: a coordinate is written into store keys, log lines and generated files, and a character that breaks a line breaks all three",
				environment, r)
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
	if err := ValidateLinkName(environment, name); err != nil {
		return 0, err
	}
	if err := ValidateOwner(owner); err != nil {
		return 0, err
	}

	claimed, err := s.claims(ctx, scope)
	if err != nil {
		return 0, err
	}
	if by, taken := otherOwner(claimed[name], owner); taken {
		return 0, s.claimRefusal(scope, name, by, owner)
	}
	if err := s.claim(ctx, scope, owner, environment, name); err != nil {
		return 0, err
	}
	return s.writePair(ctx, scope, environment, owner, name, pair)
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
	if _, err := s.Records.Write(ctx, heldValue); err != nil {
		return 0, s.racedPair(err, name)
	}
	heldRecord.Bytes = written
	if _, err := s.Records.Write(ctx, heldRecord); err != nil {
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
	if err := ValidateLinkName(environment, name); err != nil {
		return false, err
	}

	claimed, err := s.claims(ctx, scope)
	if err != nil {
		return false, err
	}
	at := canonicalEnvironment(environment)
	var owners []string
	for _, c := range claimed[name] {
		if c.environment == at && !slices.Contains(owners, c.owner) {
			owners = append(owners, c.owner)
		}
	}
	if len(owners) == 0 {
		return false, nil
	}
	slices.Sort(owners)

	for _, owner := range owners {
		if err := s.unclaim(ctx, scope, owner, environment, name); err != nil {
			return false, err
		}
	}
	for _, name := range []ports.RecordName{
		linkRecordName(scope, environment, name),
		linkValueName(scope, environment, name),
	} {
		if err := ports.Forget(ctx, s.Records, name); err != nil {
			return false, fmt.Errorf("remove %s: %w", name, err)
		}
	}
	return true, nil
}

func (s Store) ResolveLink(ctx context.Context, scope Scope, environment, name string) (Published, error) {
	if err := ValidateLinkName(environment, name); err != nil {
		return Published{}, err
	}

	for range linkAttempts {
		resolved, err := s.readPair(ctx, scope, environment, name)
		if errors.Is(err, ErrTornPair) {
			continue
		}
		return resolved, err
	}
	return Published{}, fmt.Errorf(
		"link %s's record and the value beside it came from different publishes, %d reads in a row. "+
			"A deploy is rewriting that link; nothing will be served half of one publish and half of another: %w",
		name, linkAttempts, ErrTornPair)
}

func (s Store) readPair(ctx context.Context, scope Scope, environment, name string) (Published, error) {
	for _, at := range shadowing(environment) {
		_, record, err := s.linkRecordAt(ctx, scope, at, name)
		if err != nil {
			return Published{}, err
		}
		value, err := s.linkValueAt(ctx, scope, at, name)
		if err != nil {
			return Published{}, err
		}
		if record.Version == 0 && value.Version == 0 {
			continue
		}
		if record.Version != value.Version {
			return Published{}, ErrTornPair
		}
		plaintext, err := s.Sealer.Open(ctx, linkCoordinate(scope, at, name), value.Sealed)
		if err != nil {
			return Published{}, fmt.Errorf("open link %s's value: %w", name, err)
		}
		return Published{
			Name:        name,
			Environment: at,
			Record:      record.Record,
			Shapes:      record.Shapes,
			Value:       plaintext,
			Owner:       record.owner(),
			Version:     record.Version,
			UpdatedAt:   record.UpdatedAt,
		}, nil
	}
	return Published{}, fmt.Errorf("link %s is not published to %s: %w", name, describeEnvironment(environment), ErrNotPublished)
}

func (s Store) ListLinks(ctx context.Context, scope Scope, environment string) ([]Published, error) {
	if err := ValidateLinkEnvironment(environment); err != nil {
		return nil, err
	}
	names, err := s.PublishedNames(ctx, scope, environment)
	if err != nil {
		return nil, err
	}

	out := make([]Published, 0, len(names))
	for _, name := range names {
		for _, at := range shadowing(environment) {
			_, record, err := s.linkRecordAt(ctx, scope, at, name)
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

func (s Store) claim(ctx context.Context, scope Scope, owner, environment, name string) error {
	return s.reindex(ctx, scope, owner, environment, func(names []string) []string {
		if slices.Contains(names, name) {
			return names
		}
		return append(names, name)
	})
}

func (s Store) unclaim(ctx context.Context, scope Scope, owner, environment, name string) error {
	return s.reindex(ctx, scope, owner, environment, func(names []string) []string {
		return slices.DeleteFunc(slices.Clone(names), func(held string) bool { return held == name })
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
	if len(held.Bytes) == 0 {
		return held, linkRecord{}, nil
	}
	var record linkRecord
	if err := json.Unmarshal(held.Bytes, &record); err != nil {
		return ports.Record{}, linkRecord{}, fmt.Errorf("read link %s's record: %w", name, err)
	}
	return held, record, nil
}

func (s Store) linkValueAt(ctx context.Context, scope Scope, environment, name string) (linkValue, error) {
	held, err := ports.Held(ctx, s.Records, linkValueName(scope, environment, name))
	if err != nil {
		return linkValue{}, fmt.Errorf("read link %s's value: %w", name, err)
	}
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
