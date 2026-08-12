package cloudflare

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"

	cf "github.com/cloudflare/cloudflare-go/v4"
	"github.com/cloudflare/cloudflare-go/v4/dns"
	"github.com/cloudflare/cloudflare-go/v4/workers"
	"github.com/cloudflare/cloudflare-go/v4/zones"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const (
	routeRecordContent = "100::"
	routeRecordComment = "managed by ocel — worker route placeholder"
)

type routePlan struct {
	desired        []string
	prune          bool
	pruneStem      string
	requiredRecord string
}

func (p *provider) reconcileWorkerRoutes(ctx context.Context, up upload, plan routePlan, warn func(string)) error {
	warn = nilSafeWarn(warn)
	routes := p.routeSnapshot()

	if err := p.detachCustomDomains(ctx, up.accountID, up.scriptName); err != nil {
		return err
	}

	if plan.requiredRecord != "" && len(plan.desired) > 0 {
		if err := p.verifyProxiedRecord(ctx, up.accountID, plan.requiredRecord); err != nil {
			return err
		}
	}

	wanted := make(map[string]bool, len(plan.desired))
	for _, host := range plan.desired {
		zoneID, zoneName, err := p.resolveZone(ctx, up.accountID, routeBaseDomain(host))
		if err != nil {
			return err
		}
		wanted[host] = true
		if !coveredByUniversalSSL(host, zoneName) {
			warn(fmt.Sprintf("%s is more than one label below %s, which the zone's Universal SSL certificate does not cover — TLS will fail there until you add a Cloudflare Advanced Certificate for it", host, zoneName))
		}
		if err := p.ensureRoute(ctx, routes, zoneID, RoutePattern(host), up.scriptName); err != nil {
			return err
		}
		if plan.requiredRecord != "" {
			continue
		}
		if err := p.ensureProxiedRecord(ctx, zoneID, host, warn); err != nil {
			return err
		}
	}

	if !plan.prune {
		return nil
	}
	return p.pruneStaleRoutes(ctx, routes, up, plan.pruneStem, wanted)
}

func (p *provider) pruneStaleRoutes(ctx context.Context, snap *routeSnapshot, up upload, stem string, wanted map[string]bool) error {
	owned, err := p.accountZones(ctx, up.accountID)
	if err != nil {
		return err
	}
	var errs []error
	for _, zone := range owned {
		inZone, err := snap.inZone(ctx, zone.id)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		for _, route := range slices.Clone(inZone) {
			if route.Script == edge.SharedPreviewEntryScript {
				continue
			}
			if route.Script != up.scriptName && !edge.NameUnderStem(stem, route.Script) {
				continue
			}
			host := strings.TrimSuffix(route.Pattern, "/*")
			if wanted[host] {
				continue
			}
			if _, err := p.client.Workers.Routes.Delete(ctx, route.ID, workers.RouteDeleteParams{ZoneID: cf.F(zone.id)}); err != nil {
				errs = append(errs, fmt.Errorf("delete stale worker route %q: %w", route.Pattern, err))
				continue
			}
			snap.detached(zone.id, route.ID)
			if err := p.deleteProxiedRecord(ctx, zone.id, host); err != nil {
				errs = append(errs, err)
			}
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

func canonicalDomainURL(domains []string) string {
	if len(domains) == 0 {
		return ""
	}
	for _, host := range domains {
		if !strings.HasPrefix(host, "*.") {
			return "https://" + host
		}
	}
	return "https://" + domains[0]
}

func nilSafeWarn(warn func(string)) func(string) {
	if warn == nil {
		return func(string) {}
	}
	return warn
}

func RoutePattern(hostname string) string {
	return hostname + "/*"
}

func (p *provider) RouteOwner(ctx context.Context, pattern string) (string, error) {
	accountID := os.Getenv(envAccountID)
	if accountID == "" {
		return "", fmt.Errorf("%s is not set; it is required to read Cloudflare worker routes", envAccountID)
	}
	zoneID, _, err := p.resolveZone(ctx, accountID, routeBaseDomain(strings.TrimSuffix(pattern, "/*")))
	if err != nil {
		return "", err
	}
	inZone, err := p.routeSnapshot().inZone(ctx, zoneID)
	if err != nil {
		return "", err
	}
	for _, route := range inZone {
		if route.Pattern == pattern {
			return route.Script, nil
		}
	}
	return "", nil
}

func (p *provider) ensureRoute(ctx context.Context, snap *routeSnapshot, zoneID, pattern, scriptName string) error {
	inZone, err := snap.inZone(ctx, zoneID)
	if err != nil {
		return err
	}
	for _, route := range inZone {
		if route.Pattern != pattern {
			continue
		}
		if route.Script == scriptName {
			return nil
		}
		if _, err := p.client.Workers.Routes.Update(ctx, route.ID, workers.RouteUpdateParams{
			ZoneID:  cf.F(zoneID),
			Pattern: cf.F(pattern),
			Script:  cf.F(scriptName),
		}); err != nil {
			return fmt.Errorf("repoint worker route %q: %w", pattern, err)
		}
		snap.repointed(zoneID, route.ID, scriptName)
		return nil
	}

	attached, err := p.client.Workers.Routes.New(ctx, workers.RouteNewParams{
		ZoneID:  cf.F(zoneID),
		Pattern: cf.F(pattern),
		Script:  cf.F(scriptName),
	})
	if err != nil {
		return fmt.Errorf("attach worker route %q: %w", pattern, err)
	}
	snap.attached(zoneID, workers.RouteListResponse{ID: attached.ID, Pattern: pattern, Script: scriptName})
	return nil
}

func (p *provider) verifyProxiedRecord(ctx context.Context, accountID, name string) error {
	zoneID, _, err := p.resolveZone(ctx, accountID, routeBaseDomain(name))
	if err != nil {
		return err
	}
	haveAddress, haveProxied, err := p.addressRecordsAt(ctx, zoneID, name)
	if err != nil {
		return err
	}
	switch {
	case !haveAddress:
		return fmt.Errorf("no DNS record for %q, which the hostnames this deploy serves resolve through — add a proxied (orange cloud) record at %q in Cloudflare and re-run", name, name)
	case !haveProxied:
		return fmt.Errorf("the DNS record for %q is not proxied through Cloudflare, so no worker route under it can ever fire — set %q to proxied (orange cloud) and re-run", name, name)
	}
	return nil
}

func (p *provider) addressRecordsAt(ctx context.Context, zoneID, hostname string) (haveAddress, haveProxied bool, err error) {
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

func (p *provider) ensureProxiedRecord(ctx context.Context, zoneID, hostname string, warn func(string)) error {
	haveAddress, haveProxied, err := p.addressRecordsAt(ctx, zoneID, hostname)
	if err != nil {
		return err
	}
	if haveAddress {
		if !haveProxied {
			warn(fmt.Sprintf("%s already has a DNS record that is not proxied through Cloudflare, so the worker route will not serve it — set that record to proxied (orange cloud) for %s to go live", hostname, hostname))
		}
		return nil
	}

	if _, err := p.client.DNS.Records.New(ctx, dns.RecordNewParams{
		ZoneID: cf.F(zoneID),
		Body: dns.AAAARecordParam{
			Name:    cf.F(hostname),
			Type:    cf.F(dns.AAAARecordTypeAAAA),
			Content: cf.F(routeRecordContent),
			Proxied: cf.F(true),
			TTL:     cf.F(dns.TTL(1)),
			Comment: cf.F(routeRecordComment),
		},
	}); err != nil {
		return fmt.Errorf("plant proxied DNS record for %q: %w", hostname, err)
	}
	return nil
}

func isAddressRecord(t dns.RecordResponseType) bool {
	switch t {
	case dns.RecordResponseTypeA, dns.RecordResponseTypeAAAA, dns.RecordResponseTypeCNAME:
		return true
	default:
		return false
	}
}

func (p *provider) detachRouteRecords(ctx context.Context, snap *routeSnapshot, accountID, scriptName string) error {
	owned, err := p.accountZones(ctx, accountID)
	if err != nil {
		return err
	}
	var errs []error
	for _, zone := range owned {
		inZone, err := snap.inZone(ctx, zone.id)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		for _, route := range inZone {
			if route.Script != scriptName {
				continue
			}
			hostname := strings.TrimSuffix(route.Pattern, "/*")
			if err := p.deleteProxiedRecord(ctx, zone.id, hostname); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

func (p *provider) deleteProxiedRecord(ctx context.Context, zoneID, hostname string) error {
	records := p.client.DNS.Records.ListAutoPaging(ctx, dns.RecordListParams{
		ZoneID: cf.F(zoneID),
		Name:   cf.F(dns.RecordListParamsName{Exact: cf.F(hostname)}),
		Type:   cf.F(dns.RecordListParamsTypeAAAA),
	})
	for records.Next() {
		rec := records.Current()
		if rec.Content != routeRecordContent {
			continue
		}
		if _, err := p.client.DNS.Records.Delete(ctx, rec.ID, dns.RecordDeleteParams{ZoneID: cf.F(zoneID)}); err != nil {
			return fmt.Errorf("delete DNS record %q: %w", hostname, err)
		}
	}
	if err := records.Err(); err != nil {
		return fmt.Errorf("list DNS records for %q: %w", hostname, err)
	}
	return nil
}

func (p *provider) detachCustomDomains(ctx context.Context, accountID, scriptName string) error {
	attached := p.client.Workers.Domains.ListAutoPaging(ctx, workers.DomainListParams{
		AccountID: cf.F(accountID),
		Service:   cf.F(scriptName),
	})
	for attached.Next() {
		dom := attached.Current()
		if err := p.client.Workers.Domains.Delete(ctx, dom.ID, workers.DomainDeleteParams{
			AccountID: cf.F(accountID),
		}); err != nil {
			return fmt.Errorf("detach custom domain %q: %w", dom.Hostname, err)
		}
	}
	if err := attached.Err(); err != nil {
		return fmt.Errorf("list custom domains for %q: %w", scriptName, err)
	}
	return nil
}

type zoneRef struct {
	id   string
	name string
}

func (p *provider) accountZones(ctx context.Context, accountID string) ([]zoneRef, error) {
	p.zoneMu.Lock()
	defer p.zoneMu.Unlock()
	if cached, ok := p.zonesSeen[accountID]; ok {
		return cached, nil
	}

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
	if p.zonesSeen == nil {
		p.zonesSeen = map[string][]zoneRef{}
	}
	p.zonesSeen[accountID] = refs
	return refs, nil
}

type routeSnapshot struct {
	p      *provider
	byZone map[string][]workers.RouteListResponse
}

func (p *provider) routeSnapshot() *routeSnapshot {
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

func (p *provider) resolveZone(ctx context.Context, accountID, hostname string) (id, name string, err error) {
	owned, err := p.accountZones(ctx, accountID)
	if err != nil {
		return "", "", err
	}
	for _, z := range owned {
		if zoneOwns(hostname, z.name) && len(z.name) > len(name) {
			id, name = z.id, z.name
		}
	}
	if id == "" {
		return "", "", fmt.Errorf("no Cloudflare zone in this account owns %q — add its zone to the account whose CLOUDFLARE_API_TOKEN you provided", hostname)
	}
	return id, name, nil
}

func zoneOwns(hostname, zone string) bool {
	return hostname == zone || strings.HasSuffix(hostname, "."+zone)
}
