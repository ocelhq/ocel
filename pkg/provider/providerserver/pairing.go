package providerserver

import (
	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/router"
)

func pairApps(facts provider.Facts, front edge.Kind, apps []provider.AppEntry) map[string]router.Kind {
	paired := make(map[string]router.Kind, len(apps))
	for _, entry := range apps {
		if kind, found := facts.PairedRouter(front, entry.Compute()); found {
			paired[entry.App] = kind
		}
	}
	return paired
}
