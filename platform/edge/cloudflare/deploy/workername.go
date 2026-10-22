package cloudflare

import (
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
)

const (
	maxWorkerNameLength = 63
	entryWorkerWord     = "entry"
)

var workerTiers = []environment.Tier{environment.TierProduction, environment.TierPreview}

func (n originNames) entryWorker(slug string, tier environment.Tier) (string, error) {
	if err := refuseUnknownTier("stack workers", tier); err != nil {
		return "", err
	}
	return naming.TaggedName{
		Head:     naming.OcelName(n.namespace, tier, n.origin.tag(), entryWorkerWord),
		Readable: naming.Sanitize(slug),
		Tag:      naming.HashTag(n.namespace, slug, string(tier), n.origin.Vendor, n.origin.ID),
	}.Fit(maxWorkerNameLength)
}

func (n originNames) ownsScript(slug string) (func(script string) bool, error) {
	owned := map[string]bool{}
	if slug != "" {
		for _, tier := range workerTiers {
			name, err := n.entryWorker(slug, tier)
			if err != nil {
				return nil, err
			}
			owned[name] = true
		}
	}
	return func(script string) bool { return owned[script] }, nil
}
