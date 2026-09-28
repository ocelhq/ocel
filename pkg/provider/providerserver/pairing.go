package providerserver

import (
	"fmt"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/router"
)

func pairApps(facts provider.Facts, front edge.Kind, apps []provider.AppEntry) (map[string]router.Kind, error) {
	paired := make(map[string]router.Kind, len(apps))
	for _, entry := range apps {
		kind, found := facts.PairedRouter(front, entry.Compute())
		if !found {
			continue
		}
		if kind != router.Kind(front) {
			return nil, fmt.Errorf("this provider pairs the %q edge with %q for %s apps, and a release is flipped only through the stack the edge keeps", front, kind, entry.Compute())
		}
		paired[entry.App] = kind
	}
	return paired, nil
}
