package cloudflare

import (
	"fmt"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
)

const (
	maxWorkerNameLength = 63
	entryWorkerWord     = "entry"
)

var workerTiers = []environment.Tier{environment.TierProduction, environment.TierPreview}

func entryWorkerName(namespace, slug string, tier environment.Tier) (string, error) {
	if tier != environment.TierProduction && tier != environment.TierPreview {
		return "", fmt.Errorf("stack workers: unknown tier %q", tier)
	}
	return naming.TaggedName{
		Head:     naming.OcelName(namespace, tier, entryWorkerWord),
		Readable: naming.Sanitize(slug),
		Tag:      naming.HashTag(namespace, slug, string(tier)),
	}.Fit(maxWorkerNameLength), nil
}

func projectOwnsScript(namespace, slug string) func(script string) bool {
	owned := map[string]bool{}
	if namespace != "" && slug != "" {
		for _, tier := range workerTiers {
			name, _ := entryWorkerName(namespace, slug, tier)
			owned[name] = true
		}
	}
	return func(script string) bool { return owned[script] }
}

func ProjectOwnsWorker(namespace, slug, script string) bool {
	return projectOwnsScript(namespace, slug)(script)
}
