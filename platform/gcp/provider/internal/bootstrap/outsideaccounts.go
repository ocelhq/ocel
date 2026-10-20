package bootstrap

import (
	"context"
	"fmt"
	"slices"

	"github.com/ocelhq/ocel/platform/gcp/provider/cost"
)

const (
	ReasonOutsideKept     = "the service accounts it binds belong to the project, not to ocel, and an account it unbound could be bound to either tier and acted as by that tier's deploy credential"
	reasonOutsideUnbound  = "%s carry no ocel-tier tag, and a deploy credential of either tier may bind an untagged account to its own tier and then act as it"
	reasonOutsideUnrecord = "this tier has no record of the accounts already carrying an ocel-tier tag, so each of its deploys would read the tags of every account again"
)

var outsideAccountsKind = KindOps{
	noun:      "tag value bindings of",
	terraform: cost.TagBindingType,
	survey: func(ctx context.Context, b Engine, read Survey, _ Resource) (Presence, error) {
		return b.outsideAccountsPresence(ctx, read)
	},
	Make: func(ctx context.Context, b Engine, read Survey, _ Resource) error {
		return b.bindOutsideAccounts(ctx, read)
	},
	removal: func(_ Survey, target Resource) Removal { return kept(target, ReasonOutsideKept) },
}

func (b Engine) outsideAccountsPresence(ctx context.Context, read Survey) (Presence, error) {
	if !b.Clients.TierTagged() {
		return Presence{Present: true}, nil
	}
	found, err := b.Clients.ReadOutsideAccounts(ctx, nil)
	if err != nil {
		return Presence{}, err
	}
	if len(found.Untagged) > 0 {
		return Presence{Present: true, Mends: fmt.Sprintf(reasonOutsideUnbound, found.UntaggedNamed())}, nil
	}
	recorded, err := b.Clients.RecordedOutsideAccounts(ctx, read.Tier)
	if err != nil {
		return Presence{}, err
	}
	for _, tagged := range found.Tagged {
		if !slices.Contains(recorded, tagged) {
			return Presence{Present: true, Mends: reasonOutsideUnrecord}, nil
		}
	}
	return Presence{Present: true}, nil
}

func (b Engine) bindOutsideAccounts(ctx context.Context, read Survey) error {
	if !b.Clients.TierTagged() {
		return nil
	}
	found, err := b.Clients.ReadOutsideAccounts(ctx, nil)
	if err != nil {
		return err
	}
	tagged := found.Tagged
	for _, account := range found.Untagged {
		if err := b.Clients.EnsureOutsideTag(ctx, b.Clients.AccountTagged(account.UniqueId)); err != nil {
			return fmt.Errorf("bind %s to the service account %s: %w", b.Clients.OutsideTagValuePath(), account.Email, err)
		}
		tagged = append(tagged, account.UniqueId)
	}
	return b.Clients.RecordOutsideAccounts(ctx, read.Tier, tagged)
}
