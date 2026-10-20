package alb

import (
	"context"
	"maps"
	"slices"

	"github.com/ocelhq/ocel/pkg/router"
	"github.com/ocelhq/ocel/platform/gcp/provider/pin"
)

type immutableSwitch struct{ live, next StaticRoutes }

func (s *stack) shareImmutableFiles(ctx context.Context, move router.PointerMove) error {
	switches, err := s.listImmutableSwitches(move)
	if err != nil {
		return err
	}
	for _, switching := range switches {
		if err := s.e.deps.CopyImmutableFiles(ctx, s.state.Tier, switching.live, switching.next); err != nil {
			return err
		}
		if err := s.e.deps.CopyImmutableFiles(ctx, s.state.Tier, switching.next, switching.live); err != nil {
			return err
		}
	}
	return nil
}

func (s *stack) listImmutableSwitches(move router.PointerMove) ([]immutableSwitch, error) {
	_, _, deployment := router.ParseDeploymentPointer(move.Pointer)
	var switches []immutableSwitch
	for _, hostname := range slices.Sorted(maps.Keys(s.recorded.Hosts)) {
		host := s.recorded.Hosts[hostname]
		if isWildcard(hostname) || host.Pointer != move.Pointer && (host.Pointer != "" || deployment) {
			continue
		}
		record, found := move.Records[host.App]
		if !found || pin.ResolveService(record) != host.Service {
			continue
		}
		live, err := host.decodeStaticRoutes()
		if err != nil {
			return nil, err
		}
		next, err := staticRoutesOf(record, s.recorded.LoadBalancer)
		if err != nil {
			return nil, err
		}
		if live == nil || next == nil || live.Objects == next.Objects {
			continue
		}
		switching := immutableSwitch{live: *live, next: *next}
		if !slices.ContainsFunc(switches, func(seen immutableSwitch) bool {
			return seen.live.Objects == switching.live.Objects && seen.next.Objects == switching.next.Objects
		}) {
			switches = append(switches, switching)
		}
	}
	return switches, nil
}
