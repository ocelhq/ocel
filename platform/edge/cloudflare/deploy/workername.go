package cloudflare

import (
	"context"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
)

const (
	maxWorkerNameLength = 63
	entryWorkerWord     = "entry"
)

var workerTiers = []environment.Tier{environment.TierProduction, environment.TierPreview}

func entryWorkerName(namespace string, origin OriginScope, slug string, tier environment.Tier) (string, error) {
	if err := origin.refuseIncomplete("stack workers"); err != nil {
		return "", err
	}
	if err := refuseUnknownTier("stack workers", tier); err != nil {
		return "", err
	}
	return naming.TaggedName{
		Head:     naming.OcelName(namespace, tier, origin.tag(), entryWorkerWord),
		Readable: naming.Sanitize(slug),
		Tag:      naming.HashTag(namespace, slug, string(tier), origin.Vendor, origin.ID),
	}.Fit(maxWorkerNameLength)
}

func (p *cloudflare) entryWorkerName(ctx context.Context, slug string, tier environment.Tier) (string, error) {
	origin, err := p.originScope(ctx)
	if err != nil {
		return "", err
	}
	return entryWorkerName(p.namespace, origin, slug, tier)
}

func projectOwnsScript(namespace string, origin OriginScope, slug string) (func(script string) bool, error) {
	owned := map[string]bool{}
	if namespace != "" && slug != "" {
		for _, tier := range workerTiers {
			name, err := entryWorkerName(namespace, origin, slug, tier)
			if err != nil {
				return nil, err
			}
			owned[name] = true
		}
	}
	return func(script string) bool { return owned[script] }, nil
}

func (p *cloudflare) projectOwnsScript(ctx context.Context, slug string) (func(script string) bool, error) {
	origin, err := p.originScope(ctx)
	if err != nil {
		return nil, err
	}
	return projectOwnsScript(p.namespace, origin, slug)
}
