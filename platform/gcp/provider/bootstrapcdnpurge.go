package gcp

import (
	"context"
	"slices"

	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
)

func newCDNPurgeRole(names Names) customRole {
	return customRole{
		id:          names.CDNPurgeRole(),
		title:       "ocel cache purge (" + string(names.namespace) + ")",
		description: "lets a Next app behind the alb clear its revalidated pages from Cloud CDN, and nothing else",
		permissions: []string{"compute.urlMaps.invalidateCache"},
		driftReason: "it holds other permissions than clearing Cloud CDN, which is all a Next app behind the alb is granted it for",
	}
}

func keepsCDNPurgeRole(features []string, emulated bool) bool {
	return !emulated && slices.Contains(features, albFeature)
}

func (b bootstrap) raiseCDNPurgeRole(ctx context.Context, req provider.BootstrapRequest, progress progress.Log) error {
	if !keepsCDNPurgeRole(req.Features, b.clients.emulated()) {
		return nil
	}
	names := b.clients.Names
	ensureProgress(progress).Say("Keeping custom role " + names.CDNPurgeRolePath() + " for " + string(req.Tier) +
		": Next apps behind the alb are granted it to clear Cloud CDN, and nothing else")
	return b.makeRole(ctx, newCDNPurgeRole(names))
}
