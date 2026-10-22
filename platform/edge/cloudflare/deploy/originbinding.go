package cloudflare

import (
	"context"
	"errors"
	"fmt"
	"slices"

	cf "github.com/cloudflare/cloudflare-go/v4"
	"github.com/cloudflare/cloudflare-go/v4/workers"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
)

func (s *stack) isForwarded(hostname string) bool {
	return slices.ContainsFunc(s.state.Records, func(rec edge.Record) bool { return rec.Name == hostname })
}

func (s *stack) isStillForwarded(ctx context.Context, names originNames, zoneID, hostname string) (bool, error) {
	if !s.isForwarded(hostname) {
		return false, nil
	}
	owner, err := names.forwardingOwner(s.state.Slug, s.state.Tier)
	if err != nil {
		return false, err
	}
	held, forwarded, err := s.p.readForwardingOwner(ctx, zoneID, hostname)
	return !forwarded || held == owner, err
}

func (s *stack) bindForwarded(ctx context.Context, names originNames, binding edge.DomainBinding) error {
	accountID, err := s.p.readTierAccount(ctx, s.state.Tier, fmt.Sprintf("forward %s through Cloudflare", binding.Hostname))
	if err != nil {
		return err
	}
	zone, err := s.p.resolveHostnameZone(ctx, accountID, binding.Hostname)
	if err != nil {
		return err
	}
	owner, err := names.forwardingOwner(s.state.Slug, s.state.Tier)
	if err != nil {
		return err
	}
	var served []string
	if s.state.Tier != environment.TierPreview {
		served = []string{s.own.EntryWorker}
	}
	restoreRoute, err := s.p.ensureRouteWithoutWorker(ctx, names.origin, zone.accountID, zone.zoneID, routePattern(binding.Hostname), served)
	if err != nil {
		return err
	}
	if err := s.p.bindOrigin(ctx, &s.state, zone, owner, binding); err != nil {
		return errors.Join(err, restoreRoute(ctx))
	}
	return nil
}

func (s *stack) unbindForwarded(ctx context.Context, names originNames, accountID, hostname string) error {
	zone, err := s.p.resolveHostnameZone(ctx, accountID, hostname)
	if err != nil {
		return err
	}
	owner, err := names.forwardingOwner(s.state.Slug, s.state.Tier)
	if err != nil {
		return err
	}
	forwarded, err := s.p.isForwardedBy(ctx, zone.zoneID, owner, hostname)
	if err != nil {
		return err
	}
	if forwarded {
		if err := s.p.detachRoute(ctx, zone.zoneID, routePattern(hostname), []string{""}); err != nil {
			return err
		}
	}
	return s.p.unbindOrigin(ctx, &s.state, zone, owner, hostname)
}

func (s *stack) stopForwarding(ctx context.Context, names originNames, zone zoneAccess, hostname string) error {
	owner, err := names.forwardingOwner(s.state.Slug, s.state.Tier)
	if err != nil {
		return err
	}
	if err := s.p.unforwardRecords(ctx, zone.zoneID, owner, hostname); err != nil {
		return err
	}
	if err := s.p.releaseClientCertificate(ctx, zone, names.origin, hostname); err != nil {
		return err
	}
	s.state.PublishAddress(hostname, "")
	s.state.RecordWrites(removeRecordsNamed(s.state.Records, hostname))
	return nil
}

func (p *cloudflare) ensureRouteWithoutWorker(ctx context.Context, origin OriginScope, accountID, zoneID, pattern string, served []string) (func(context.Context) error, error) {
	snap := p.routeSnapshot()
	inZone, err := snap.inZone(ctx, zoneID)
	if err != nil {
		return nil, err
	}
	for _, route := range inZone {
		if route.Pattern != pattern {
			continue
		}
		if route.Script == "" {
			return func(context.Context) error { return nil }, nil
		}
		if slices.Contains(served, route.Script) {
			if _, err := p.client.Workers.Routes.Update(ctx, route.ID, workers.RouteUpdateParams{ZoneID: cf.F(zoneID), Pattern: cf.F(pattern)}); err != nil {
				return nil, fmt.Errorf("repoint worker route %q to run no worker: %w", pattern, err)
			}
			snap.repointed(zoneID, route.ID, "")
			return func(ctx context.Context) error {
				if _, err := p.client.Workers.Routes.Update(ctx, route.ID, workers.RouteUpdateParams{ZoneID: cf.F(zoneID), Pattern: cf.F(pattern), Script: cf.F(route.Script)}); err != nil {
					return fmt.Errorf("repoint worker route %q back to %q: %w", pattern, route.Script, err)
				}
				snap.repointed(zoneID, route.ID, route.Script)
				return nil
			}, nil
		}
		held, other, err := p.readOtherOrigin(ctx, origin, accountID, route.Script)
		if err != nil {
			return nil, err
		}
		if other {
			return nil, refuseOtherOriginsRoute(pattern, route.Script, held)
		}
		return nil, fmt.Errorf("worker route %q runs %q, and a hostname forwarded to its origin runs no worker: remove that route and bind it again", pattern, route.Script)
	}
	attached, err := p.client.Workers.Routes.New(ctx, workers.RouteNewParams{ZoneID: cf.F(zoneID), Pattern: cf.F(pattern)})
	if err != nil {
		return nil, fmt.Errorf("attach the route that runs no worker on %q: %w", pattern, err)
	}
	snap.attached(zoneID, workers.RouteListResponse{ID: attached.ID, Pattern: pattern})
	return func(ctx context.Context) error {
		if _, err := p.client.Workers.Routes.Delete(ctx, attached.ID, workers.RouteDeleteParams{ZoneID: cf.F(zoneID)}); err != nil {
			return fmt.Errorf("remove the route that runs no worker on %q: %w", pattern, err)
		}
		snap.detached(zoneID, attached.ID)
		return nil
	}, nil
}
