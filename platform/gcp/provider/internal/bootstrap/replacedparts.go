package bootstrap

import (
	"context"
	"slices"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
)

type partReplacement struct {
	part  ResourcePart
	read  Survey
	items []Resource
}

type ReplacedParts []partReplacement

func (p ResourcePart) replaceable(b Engine, tier environment.Tier) []Resource {
	items := p.provisionedItems(b, tier)
	if !slices.ContainsFunc(items, func(item Resource) bool { return item.KindOps().replaceable }) {
		return nil
	}
	return items
}

func (b Engine) SurveyReplacedParts(ctx context.Context, base Survey) (ReplacedParts, error) {
	tier := base.Tier
	var replaced ReplacedParts
	for _, each := range DeployedParts() {
		items := each.replaceable(b, tier)
		if len(items) == 0 {
			continue
		}
		off, err := b.ListServicesOff(ctx, each.APIs)
		if err != nil {
			return nil, err
		}
		if len(off) > 0 {
			continue
		}
		read := b.newSurvey(tier)
		read.Buckets = base.Buckets
		if err := b.surveyPresence(ctx, read, items); err != nil {
			return nil, err
		}
		if len(replacedIn(read, items)) > 0 {
			replaced = append(replaced, partReplacement{part: each, read: read, items: items})
		}
	}
	return replaced, nil
}

func (r ReplacedParts) Names() []string {
	names := make([]string, 0, len(r))
	for _, each := range r {
		names = append(names, each.part.Name)
	}
	return names
}

func (r ReplacedParts) Replacing() []string {
	var replacing []string
	for _, each := range r {
		replacing = append(replacing, replacedIn(each.read, each.items)...)
	}
	return replacing
}

func (r ReplacedParts) Planned(b Engine) []provider.ChangeGroup {
	groups := make([]provider.ChangeGroup, 0, len(r))
	for _, each := range r {
		group := provider.ChangeGroup{
			Kind:    provider.StackGroupKind,
			Name:    b.partGroupName(each.read.Tier, each.part.Name),
			Part:    each.part.Name,
			Changes: Planned(each.read, each.items),
		}
		group.Action, group.Reason = provider.RollUp(group.Changes)
		groups = append(groups, group)
	}
	return groups
}

func (b Engine) ReplaceParts(ctx context.Context, tier environment.Tier, replaced ReplacedParts) error {
	if len(replaced) == 0 {
		return nil
	}
	return b.ensureParts(ctx, tier, "", replaced.Names(), true)
}
