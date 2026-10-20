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

func entryWorkerName(namespace, slug string, tier environment.Tier) (string, error) {
	if err := refuseUnknownTier("stack workers", tier); err != nil {
		return "", err
	}
	return naming.TaggedName{
		Head:     naming.OcelName(namespace, tier, entryWorkerWord),
		Readable: naming.Sanitize(slug),
		Tag:      naming.HashTag(namespace, slug, string(tier)),
	}.Fit(maxWorkerNameLength)
}

func projectOwnsScript(namespace, slug string) (func(script string) bool, error) {
	owned := map[string]bool{}
	if namespace != "" && slug != "" {
		for _, tier := range workerTiers {
			name, err := entryWorkerName(namespace, slug, tier)
			if err != nil {
				return nil, err
			}
			owned[name] = true
		}
	}
	return func(script string) bool { return owned[script] }, nil
}

func ProjectOwnsWorker(namespace, slug, script string) (bool, error) {
	owns, err := projectOwnsScript(namespace, slug)
	if err != nil {
		return false, err
	}
	return owns(script), nil
}
