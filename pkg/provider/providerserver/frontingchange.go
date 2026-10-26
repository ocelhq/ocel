package providerserver

import (
	"context"
	"fmt"
	"maps"
	"slices"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/progress"
)

func (r *deployRun) rebindHostsWhoseFrontingChanged(ctx context.Context, progress progress.Log) {
	rebinding := &hostnames{edgeSession: r.edgeSession}
	for _, host := range slices.Sorted(maps.Keys(r.state.Hosts)) {
		hostState := r.state.Host(host)
		if hostState.Edge != r.front.Kind() || hostState.App == "" || !slices.Contains(r.edgeStack().State().Bound, host) {
			continue
		}
		target := ConfiguredHost{Hostname: host, App: hostState.App, Pointer: hostState.Pointer}
		kind, err := rebinding.readTargetRouter(target)
		if err != nil {
			continue
		}
		fronted := r.isFrontedRouter(kind)
		if hostState.Fronted == fronted {
			continue
		}
		progress.Say(describeFrontingChange(host, r.front.Kind(), fronted))
		if err := rebinding.bindOrigin(ctx, target, &hostState, progress); err != nil {
			progress.Warn(fmt.Sprintf("%s still answers as it did before this deploy: binding it again failed, so the next deploy tries again: %v", host, err))
		}
	}
}

func describeFrontingChange(host string, front edge.Kind, fronted bool) string {
	if fronted {
		return fmt.Sprintf("Binding %s again: %s fronts it now", host, describeFront(front))
	}
	return fmt.Sprintf("Binding %s again: %s no longer fronts it", host, describeFront(front))
}
