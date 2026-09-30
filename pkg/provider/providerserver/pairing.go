package providerserver

import (
	"maps"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/router"
)

func pairApps(facts provider.Facts, front edge.Kind, apps []provider.AppEntry) (map[string]router.Kind, error) {
	paired := make(map[string]router.Kind, len(apps))
	for _, entry := range apps {
		compute := entry.Compute()
		kind, found := facts.PairedRouter(front, compute)
		if !found {
			return nil, refusal.Refuse(refusal.CodeInvalid,
				"app %s runs as %s, and %s reaches no %s app on this provider: front this project with %s, or change %s's compute",
				entry.App, compute, describeFront(front), compute, describeEdges(listPairedEdges(facts, compute)), entry.App)
		}
		paired[entry.App] = kind
	}
	return paired, nil
}

func listPairedEdges(facts provider.Facts, compute provider.Compute) []edge.Kind {
	var kinds []edge.Kind
	for _, pairing := range facts.Pairings {
		if slices.Contains(pairing.Computes, compute) && !slices.Contains(kinds, pairing.Edge) {
			kinds = append(kinds, pairing.Edge)
		}
	}
	slices.Sort(kinds)
	return kinds
}

func describeEdges(kinds []edge.Kind) string {
	if len(kinds) == 0 {
		return "an edge this provider offers"
	}
	described := make([]string, 0, len(kinds))
	for _, kind := range kinds {
		described = append(described, describeFront(kind))
	}
	return strings.Join(described, " or ")
}

func (r *deployRun) pairRouters() {
	byRouter := map[router.Kind][]string{}
	for app, kind := range r.appRouters {
		byRouter[kind] = append(byRouter[kind], app)
	}
	for _, kind := range slices.Sorted(maps.Keys(byRouter)) {
		r.state.Pair(kind, r.routers[kind].record(), byRouter[kind])
	}
	r.recordRouterStates(&r.state)
}

func (r *deployRun) isEdgeRouted(app string) bool {
	return r.appRouters[app] == r.edgeKind
}

func (r *deployRun) listEdgeRoutedDomains() ([]string, map[string]string) {
	owners := r.domainApps()
	var domains []string
	for _, host := range r.hostnames() {
		if app := owners[strings.ToLower(host)]; app == "" || r.isEdgeRouted(app) {
			domains = append(domains, host)
		}
	}
	maps.DeleteFunc(owners, func(_, app string) bool { return !r.isEdgeRouted(app) })
	if len(owners) == 0 {
		owners = nil
	}
	return domains, owners
}
