package variablestore

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/pkg/records"
)

type ownerIndex struct {
	Names []string `json:"names,omitempty"`
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

type Claim struct {
	Owner       string
	Environment string
}

func (s Store) FindClaimsByOthers(ctx context.Context, scope Scope, owner string, names []string) (map[string]Claim, error) {
	claimed, err := s.claims(ctx, scope)
	if err != nil {
		return nil, err
	}
	found := map[string]Claim{}
	for _, name := range names {
		if by, taken := otherOwner(claimed[name], owner); taken {
			found[name] = by
		}
	}
	return found, nil
}

func otherOwner(claims []claim, owner string) (Claim, bool) {
	others := make([]claim, 0, len(claims))
	for _, c := range claims {
		if c.owner != owner {
			others = append(others, c)
		}
	}
	if len(others) == 0 {
		return Claim{}, false
	}
	slices.SortFunc(others, func(a, b claim) int {
		if (a.owner == OwnerOcel) != (b.owner == OwnerOcel) {
			if a.owner == OwnerOcel {
				return -1
			}
			return 1
		}
		return cmp.Or(strings.Compare(a.owner, b.owner), strings.Compare(a.environment, b.environment))
	})
	return Claim{Owner: others[0].owner, Environment: plainEnvironment(others[0].environment)}, true
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
	err := records.RetryWhileStale(ctx, func() (bool, error) {
		recorded, err := records.ReadOrEmpty(ctx, s.KeyValues, at)
		if err != nil {
			return false, fmt.Errorf("read %s's published bindings: %w", owner, err)
		}
		var index ownerIndex
		if len(recorded.Value) > 0 {
			if err := json.Unmarshal(recorded.Value, &index); err != nil {
				return false, fmt.Errorf("read %s's published bindings: %w", owner, err)
			}
		}
		kept := apply(index.Names)
		if slices.Equal(kept, index.Names) {
			return false, nil
		}
		slices.Sort(kept)

		if len(kept) == 0 {
			err := s.KeyValues.Remove(ctx, at, recorded.Revision)
			if errors.Is(err, records.ErrStale) {
				return true, nil
			}
			if err != nil && !errors.Is(err, records.ErrNotFound) {
				return false, fmt.Errorf("record %s's published bindings: %w", owner, err)
			}
			return false, nil
		}
		encoded, err := json.Marshal(ownerIndex{Names: kept})
		if err != nil {
			return false, fmt.Errorf("encode %s's published bindings: %w", owner, err)
		}
		recorded.Value = encoded
		_, err = s.KeyValues.Write(ctx, recorded)
		if errors.Is(err, records.ErrStale) {
			return true, nil
		}
		if err != nil {
			return false, fmt.Errorf("record %s's published bindings: %w", owner, err)
		}
		return false, nil
	})
	if errors.Is(err, records.ErrContended) {
		return fmt.Errorf(
			"another deploy of %s kept rewriting its published bindings under every attempt this one made to record its own. "+
				"Two deploys of the same environment are racing; run them one after the other",
			scope.Project)
	}
	return err
}
