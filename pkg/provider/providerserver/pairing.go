package providerserver

import (
	"maps"
	"slices"
	"strings"

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

func (r *deployRun) pairRouters() {
	byRouter := map[router.Kind]map[string]router.Kind{}
	for app, kind := range r.appRouters {
		if byRouter[kind] == nil {
			byRouter[kind] = map[string]router.Kind{}
		}
		byRouter[kind][app] = kind
	}
	for _, kind := range slices.Sorted(maps.Keys(byRouter)) {
		r.state.Pair(kind, r.routerState(kind), byRouter[kind])
	}
	r.recordRouterStates(&r.state)
}

func (r *deployRun) isEdgeRouted(app string) bool {
	return r.routerOf(r.appRouters[app]).Kind() == r.router.Kind()
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
