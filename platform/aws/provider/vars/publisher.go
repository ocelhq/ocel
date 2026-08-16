package vars

import (
	"context"
	"errors"
	"fmt"
	"slices"

	linksv1 "github.com/ocelhq/ocel/pkg/proto/links/v1"
)

var ErrUnsourced = errors.New("vars: unsourced link")

var ErrClaimed = errors.New("vars: link claimed by another publisher")

func ownerOf(i item) string {
	if i.Owner == "" {
		return OwnerOcel
	}
	return i.Owner
}

func describeOwner(owner string) string {
	if owner == OwnerOcel {
		return "ocel's own provisioning"
	}
	return "publisher " + owner
}

func (s *Store) PublishFor(ctx context.Context, slug, publisher, environment string, records []*linksv1.Link) (PublishResult, error) {
	if err := validatePublisher(publisher); err != nil {
		return PublishResult{}, err
	}
	for _, r := range records {
		if r.GetSource() == "" {
			return PublishResult{}, fmt.Errorf(
				"publisher %s leaves link %s unsourced: an empty source names what ocel's own provisioning produces, and an app binds a client to it on that promise. "+
					"Name the tool that publishes it: %w",
				publisher, r.GetName(), ErrUnsourced)
		}
	}
	return s.publish(ctx, slug, publisher, environment, records)
}

func (s *Store) PruneFor(ctx context.Context, slug, publisher, environment string) (PublishResult, error) {
	if err := validatePublisher(publisher); err != nil {
		return PublishResult{}, err
	}
	return s.publish(ctx, slug, publisher, environment, nil)
}

func validatePublisher(publisher string) error {
	if err := validateOwner(publisher); err != nil {
		return err
	}
	if publisher == OwnerOcel {
		return fmt.Errorf("publisher name %q names ocel's own provisioning; every record it stamps would be one ocel's next deploy may prune", OwnerOcel)
	}
	return nil
}

type claim struct {
	owner       string
	environment string
}

func (s *Store) claims(ctx context.Context, slug string) (map[string][]claim, error) {
	rows, err := s.queryConsistent(ctx, PartitionKey(slug, s.Class), linkIndexPrefix)
	if err != nil {
		return nil, err
	}

	out := map[string][]claim{}
	for _, row := range rows {
		owner, at, ok := parseLinkIndexSortKey(row.SK)
		if !ok {
			continue
		}
		for _, name := range row.Names {
			out[name] = append(out[name], claim{owner: owner, environment: at})
		}
	}
	return out, nil
}

func (s *Store) refuseClaimed(owner string, names []string, claimed map[string][]claim) error {
	if owner == OwnerOcel {
		return nil
	}
	for _, name := range names {
		by, taken := otherOwner(claimed[name], owner)
		if !taken {
			continue
		}
		return fmt.Errorf(
			"link %s in %s is already published by %s: one link name belongs to one publisher, or a pair published to a named environment silently shadows what the other publishes class-wide. "+
				"Give one of them another name: %w",
			name, s.Class, describeOwner(by), ErrClaimed)
	}
	return nil
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

func claimedByOthers(owner, environment string, claimed map[string][]claim) map[string]bool {
	out := map[string]bool{}
	for name, claims := range claimed {
		for _, c := range claims {
			if c.owner != owner && c.environment == canonicalEnvironment(environment) {
				out[name] = true
			}
		}
	}
	return out
}
