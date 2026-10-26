package cloudflare

import (
	"context"
	"errors"
	"fmt"
	"slices"

	cf "github.com/cloudflare/cloudflare-go/v4"
	"github.com/cloudflare/cloudflare-go/v4/workers"

	"github.com/ocelhq/ocel/pkg/edge"
)

const cachesContainersBinding = "OCEL_CACHES_CONTAINERS"

func (p *cloudflare) listFrontedHosts(spec edge.StackSpec) []string {
	if !p.options.Cache || (spec.PruneOnly && spec.GlobalPreview == "") {
		return nil
	}
	return spec.Fronted
}

func frontingWorker(spec edge.StackSpec, program entryProgram, names originNames, fronted []string) string {
	switch {
	case len(fronted) == 0:
		return ""
	case spec.PruneOnly:
		return names.sharedPreviewEntry()
	}
	return program.name
}

type frontedRoutes struct {
	hosts        []string
	by           string
	previously   []string
	previouslyBy string
}

func (p *cloudflare) reconcileFrontedRoutes(ctx context.Context, accountID string, prior edge.StackState, routes frontedRoutes) error {
	snap := p.routeSnapshot()
	for _, host := range routes.hosts {
		if !isRecordedForward(prior, host) {
			continue
		}
		from := []string{""}
		if routes.previouslyBy != "" && routes.previouslyBy != routes.by {
			from = append(from, routes.previouslyBy)
		}
		for _, script := range from {
			if err := p.repointHostRoute(ctx, snap, accountID, host, script, routes.by); err != nil {
				return err
			}
		}
	}
	for _, host := range routes.previously {
		if slices.Contains(routes.hosts, host) || routes.previouslyBy == "" {
			continue
		}
		if err := p.repointHostRoute(ctx, snap, accountID, host, routes.previouslyBy, ""); err != nil {
			return err
		}
	}
	return nil
}

func (p *cloudflare) repointHostRoute(ctx context.Context, snap *routeSnapshot, accountID, host, from, to string) error {
	zoneID, _, err := p.resolveZone(ctx, accountID, routeBaseDomain(host))
	if errors.Is(err, errNoZone) {
		return nil
	}
	if err != nil {
		return err
	}
	_, err = p.repointZoneRoute(ctx, snap, zoneID, host, from, to)
	return err
}

func (p *cloudflare) repointZoneRoute(ctx context.Context, snap *routeSnapshot, zoneID, host, from, to string) (bool, error) {
	inZone, err := snap.inZone(ctx, zoneID)
	if err != nil {
		return false, err
	}
	pattern := routePattern(host)
	for _, route := range inZone {
		if route.Pattern == pattern && route.Script == from {
			return true, p.repointRoute(ctx, snap, zoneID, route.ID, pattern, to)
		}
	}
	return false, nil
}

func isRecordedForward(state edge.StackState, hostname string) bool {
	return slices.ContainsFunc(state.Records, func(rec edge.Record) bool { return rec.Name == hostname })
}

func (p *cloudflare) repointRoute(ctx context.Context, snap *routeSnapshot, zoneID, routeID, pattern, script string) error {
	params := workers.RouteUpdateParams{ZoneID: cf.F(zoneID), Pattern: cf.F(pattern)}
	if script != "" {
		params.Script = cf.F(script)
	}
	if _, err := p.client.Workers.Routes.Update(ctx, routeID, params); err != nil {
		return fmt.Errorf("repoint worker route %q: %w", pattern, err)
	}
	snap.repointed(zoneID, routeID, script)
	return nil
}
