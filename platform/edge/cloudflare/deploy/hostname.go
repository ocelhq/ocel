package cloudflare

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	cf "github.com/cloudflare/cloudflare-go/v4"
	"github.com/cloudflare/cloudflare-go/v4/dns"
	"github.com/cloudflare/cloudflare-go/v4/workers"
	"github.com/cloudflare/cloudflare-go/v4/zones"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
)

type routeSpec struct {
	desired         []string
	bound           []string
	servedElsewhere []string
	routed          []string
	owns            func(script string) bool
}

func (spec routeSpec) ownsRoute(scriptName, owner string) bool {
	return owner == scriptName || spec.owns != nil && spec.owns(owner)
}

func (p *cloudflare) reconcileWorkerRoutes(ctx context.Context, up upload, spec routeSpec, warn func(string)) error {
	warn = nilSafeWarn(warn)
	routes := p.routeSnapshot()

	wanted := make(map[string]bool, len(spec.desired)+len(spec.bound))
	for _, host := range spec.bound {
		wanted[host] = true
	}
	for _, host := range spec.servedElsewhere {
		wanted[host] = true
	}
	for _, host := range spec.desired {
		zoneID, zoneName, err := p.resolveZone(ctx, up.accountID, routeBaseDomain(host))
		if err != nil {
			return err
		}
		wanted[host] = true
		if !coveredByUniversalSSL(host, zoneName) {
			warn(fmt.Sprintf("%s is more than one label below %s, which the zone's Universal SSL certificate does not cover — TLS will fail there until you add a Cloudflare Advanced Certificate for it", host, zoneName))
		}
		if err := p.ensureRoute(ctx, routes, zoneID, routePattern(host), up.scriptName, spec); err != nil {
			return err
		}
	}

	return p.pruneStaleRoutes(ctx, routes, up, spec.routed, wanted)
}

func (p *cloudflare) pruneStaleRoutes(ctx context.Context, snap *routeSnapshot, up upload, routed []string, wanted map[string]bool) error {
	var errs []error
	for _, host := range routed {
		if wanted[host] {
			continue
		}
		zoneID, _, err := p.resolveZone(ctx, up.accountID, routeBaseDomain(host))
		if errors.Is(err, errNoZone) {
			continue
		}
		if err != nil {
			errs = append(errs, err)
			continue
		}
		inZone, err := snap.inZone(ctx, zoneID)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		pattern := routePattern(host)
		for _, route := range slices.Clone(inZone) {
			if route.Pattern != pattern || route.Script != up.scriptName || route.Script == previewEntryScript {
				continue
			}
			if _, err := p.client.Workers.Routes.Delete(ctx, route.ID, workers.RouteDeleteParams{ZoneID: cf.F(zoneID)}); err != nil {
				errs = append(errs, fmt.Errorf("delete stale worker route %q: %w", route.Pattern, err))
				continue
			}
			snap.detached(zoneID, route.ID)
		}
	}
	return errors.Join(errs...)
}

func routeBaseDomain(host string) string {
	return strings.TrimPrefix(host, "*.")
}

func coveredByUniversalSSL(host, zone string) bool {
	if host == zone {
		return true
	}
	sub := strings.TrimSuffix(host, "."+zone)
	if sub == host {
		return false
	}
	return !strings.Contains(sub, ".")
}

func nilSafeWarn(warn func(string)) func(string) {
	if warn == nil {
		return func(string) {}
	}
	return warn
}

func routePattern(hostname string) string {
	return hostname + "/*"
}

func (p *cloudflare) ProjectOwner(slug string, tier environment.Tier) (string, error) {
	return entryWorkerName(p.namespace, slug, tier)
}

func (p *cloudflare) DomainOwner(ctx context.Context, hostname string) (string, error) {
	accountID, err := requireAccountID("read Cloudflare worker routes")
	if err != nil {
		return "", err
	}
	zoneID, _, err := p.resolveZone(ctx, accountID, routeBaseDomain(hostname))
	if err != nil {
		return "", err
	}
	inZone, err := p.routeSnapshot().inZone(ctx, zoneID)
	if err != nil {
		return "", err
	}
	pattern := routePattern(hostname)
	for _, route := range inZone {
		if route.Pattern != pattern || route.Script == "" {
			continue
		}
		if route.Script == previewEntryScript {
			return edge.PreviewEntryOwner, nil
		}
		return route.Script, nil
	}
	return p.readForwardedOwner(ctx, zoneID, hostname)
}

func (p *cloudflare) ensureRoute(ctx context.Context, snap *routeSnapshot, zoneID, pattern, scriptName string, spec routeSpec) error {
	if held, err := p.repointHeldRoute(ctx, snap, zoneID, pattern, scriptName, spec); err != nil || held {
		return err
	}

	attached, err := p.client.Workers.Routes.New(ctx, workers.RouteNewParams{
		ZoneID:  cf.F(zoneID),
		Pattern: cf.F(pattern),
		Script:  cf.F(scriptName),
	})
	if hasStatus(err, http.StatusConflict) {
		snap.discard(zoneID)
		held, heldErr := p.repointHeldRoute(ctx, snap, zoneID, pattern, scriptName, spec)
		if heldErr != nil || held {
			return heldErr
		}
	}
	if err != nil {
		return fmt.Errorf("attach worker route %q: %w", pattern, err)
	}
	snap.attached(zoneID, workers.RouteListResponse{ID: attached.ID, Pattern: pattern, Script: scriptName})
	return nil
}

func (p *cloudflare) repointHeldRoute(ctx context.Context, snap *routeSnapshot, zoneID, pattern, scriptName string, spec routeSpec) (bool, error) {
	inZone, err := snap.inZone(ctx, zoneID)
	if err != nil {
		return false, err
	}
	for _, route := range inZone {
		if route.Pattern != pattern {
			continue
		}
		if route.Script == scriptName {
			return true, nil
		}
		if !spec.ownsRoute(scriptName, route.Script) {
			return false, fmt.Errorf("worker route %q is owned by %q, which this project does not own, so it is left in place; release it from the project that owns it first", pattern, route.Script)
		}
		if _, err := p.client.Workers.Routes.Update(ctx, route.ID, workers.RouteUpdateParams{
			ZoneID:  cf.F(zoneID),
			Pattern: cf.F(pattern),
			Script:  cf.F(scriptName),
		}); err != nil {
			return false, fmt.Errorf("repoint worker route %q: %w", pattern, err)
		}
		snap.repointed(zoneID, route.ID, scriptName)
		return true, nil
	}
	return false, nil
}

func (p *cloudflare) addressRecordsAt(ctx context.Context, zoneID, hostname string) (haveAddress, haveProxied bool, err error) {
	existing := p.client.DNS.Records.ListAutoPaging(ctx, dns.RecordListParams{
		ZoneID: cf.F(zoneID),
		Name:   cf.F(dns.RecordListParamsName{Exact: cf.F(hostname)}),
	})
	for existing.Next() {
		rec := existing.Current()
		if !isAddressRecord(rec.Type) {
			continue
		}
		haveAddress = true
		if rec.Proxied {
			haveProxied = true
		}
	}
	if err := existing.Err(); err != nil {
		return false, false, fmt.Errorf("list DNS records for %q: %w", hostname, err)
	}
	return haveAddress, haveProxied, nil
}

func isAddressRecord(t dns.RecordResponseType) bool {
	switch t {
	case dns.RecordResponseTypeA, dns.RecordResponseTypeAAAA, dns.RecordResponseTypeCNAME:
		return true
	default:
		return false
	}
}

type zoneRef struct {
	id   string
	name string
}

func (p *cloudflare) accountZones(ctx context.Context, accountID string) ([]zoneRef, error) {
	owned := p.client.Zones.ListAutoPaging(ctx, zones.ZoneListParams{
		Account: cf.F(zones.ZoneListParamsAccount{ID: cf.F(accountID)}),
	})
	var refs []zoneRef
	for owned.Next() {
		z := owned.Current()
		refs = append(refs, zoneRef{id: z.ID, name: z.Name})
	}
	if err := owned.Err(); err != nil {
		return nil, fmt.Errorf("list zones: %w", err)
	}
	return refs, nil
}

type routeSnapshot struct {
	p      *cloudflare
	byZone map[string][]workers.RouteListResponse
}

func (p *cloudflare) routeSnapshot() *routeSnapshot {
	return &routeSnapshot{p: p, byZone: map[string][]workers.RouteListResponse{}}
}

func (s *routeSnapshot) inZone(ctx context.Context, zoneID string) ([]workers.RouteListResponse, error) {
	if cached, ok := s.byZone[zoneID]; ok {
		return cached, nil
	}
	iter := s.p.client.Workers.Routes.ListAutoPaging(ctx, workers.RouteListParams{ZoneID: cf.F(zoneID)})
	routes := []workers.RouteListResponse{}
	for iter.Next() {
		routes = append(routes, iter.Current())
	}
	if err := iter.Err(); err != nil {
		return nil, fmt.Errorf("list worker routes in zone %s: %w", zoneID, err)
	}
	s.byZone[zoneID] = routes
	return routes, nil
}

func (s *routeSnapshot) discard(zoneID string) {
	delete(s.byZone, zoneID)
}

func (s *routeSnapshot) attached(zoneID string, route workers.RouteListResponse) {
	s.byZone[zoneID] = append(s.byZone[zoneID], route)
}

func (s *routeSnapshot) repointed(zoneID, routeID, scriptName string) {
	for i, route := range s.byZone[zoneID] {
		if route.ID == routeID {
			s.byZone[zoneID][i].Script = scriptName
		}
	}
}

func (s *routeSnapshot) detached(zoneID, routeID string) {
	s.byZone[zoneID] = slices.DeleteFunc(s.byZone[zoneID], func(route workers.RouteListResponse) bool {
		return route.ID == routeID
	})
}

var errNoZone = errors.New("no Cloudflare zone in this account owns the hostname")

func (p *cloudflare) resolveZone(ctx context.Context, accountID, hostname string) (id, name string, err error) {
	for candidate := hostname; strings.Contains(candidate, "."); candidate = candidate[strings.Index(candidate, ".")+1:] {
		zone, found, err := p.zoneNamed(ctx, accountID, candidate)
		if err != nil {
			return "", "", err
		}
		if found {
			return zone.id, zone.name, nil
		}
	}
	return "", "", fmt.Errorf("%w %q — add its zone to the account whose CLOUDFLARE_API_TOKEN you provided", errNoZone, hostname)
}

func (p *cloudflare) zoneNamed(ctx context.Context, accountID, name string) (zoneRef, bool, error) {
	key := accountID + "/" + name
	p.zoneMu.Lock()
	defer p.zoneMu.Unlock()
	if cached, ok := p.zonesSeen[key]; ok {
		return cached, cached.id != "", nil
	}
	named := p.client.Zones.ListAutoPaging(ctx, zones.ZoneListParams{
		Account: cf.F(zones.ZoneListParamsAccount{ID: cf.F(accountID)}),
		Name:    cf.F(name),
	})
	var found zoneRef
	for named.Next() {
		if z := named.Current(); z.Name == name {
			found = zoneRef{id: z.ID, name: z.Name}
		}
	}
	if err := named.Err(); err != nil {
		return zoneRef{}, false, fmt.Errorf("list the zone named %q: %w", name, err)
	}
	if p.zonesSeen == nil {
		p.zonesSeen = map[string]zoneRef{}
	}
	p.zonesSeen[key] = found
	return found, found.id != "", nil
}
