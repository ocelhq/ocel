package cloudflare

import (
	"context"
	"fmt"
	"slices"

	cf "github.com/cloudflare/cloudflare-go/v4"
	"github.com/cloudflare/cloudflare-go/v4/workers"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
)

func (s *stack) formatOwner() string {
	return formatForwardingOwner(s.p.namespace, s.state.Slug, s.state.Tier)
}

func (s *stack) isForwarded(hostname string) bool {
	return slices.ContainsFunc(s.state.Records, func(rec edge.Record) bool { return rec.Name == hostname })
}

func (s *stack) bindForwarded(ctx context.Context, binding edge.DomainBinding) error {
	accountID, err := requireAccountID(fmt.Sprintf("forward %s through Cloudflare", binding.Hostname))
	if err != nil {
		return err
	}
	zoneID, _, err := s.p.resolveZone(ctx, accountID, routeBaseDomain(binding.Hostname))
	if err != nil {
		return err
	}
	pattern := routePattern(binding.Hostname)
	if s.state.Tier != environment.TierPreview {
		if err := s.p.detachRoute(ctx, zoneID, pattern, s.own.EntryWorkers); err != nil {
			return err
		}
	}
	if err := s.p.ensureRouteWithoutWorker(ctx, zoneID, pattern); err != nil {
		return err
	}
	return s.p.bindOrigin(ctx, &s.state, s.formatOwner(), binding)
}

func (s *stack) unbindForwarded(ctx context.Context, hostname string) error {
	accountID, err := requireAccountID(fmt.Sprintf("stop forwarding %s through Cloudflare", hostname))
	if err != nil {
		return err
	}
	zoneID, _, err := s.p.resolveZone(ctx, accountID, routeBaseDomain(hostname))
	if err != nil {
		return err
	}
	if err := s.p.detachRoute(ctx, zoneID, routePattern(hostname), []string{""}); err != nil {
		return err
	}
	return s.p.unbindOrigin(ctx, &s.state, s.formatOwner(), hostname)
}

func (p *cloudflare) ensureRouteWithoutWorker(ctx context.Context, zoneID, pattern string) error {
	snap := p.routeSnapshot()
	inZone, err := snap.inZone(ctx, zoneID)
	if err != nil {
		return err
	}
	for _, route := range inZone {
		if route.Pattern != pattern {
			continue
		}
		if route.Script == "" {
			return nil
		}
		return fmt.Errorf("worker route %q runs %q, and a hostname forwarded to its origin runs no worker: remove that route and bind it again", pattern, route.Script)
	}
	attached, err := p.client.Workers.Routes.New(ctx, workers.RouteNewParams{ZoneID: cf.F(zoneID), Pattern: cf.F(pattern)})
	if err != nil {
		return fmt.Errorf("attach the route that runs no worker on %q: %w", pattern, err)
	}
	snap.attached(zoneID, workers.RouteListResponse{ID: attached.ID, Pattern: pattern})
	return nil
}
