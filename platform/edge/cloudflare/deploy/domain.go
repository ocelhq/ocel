package cloudflare

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"

	cf "github.com/cloudflare/cloudflare-go/v4"
	"github.com/cloudflare/cloudflare-go/v4/ssl"
	"github.com/cloudflare/cloudflare-go/v4/workers"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const stackKeyEntryWorker = "entryWorker"

func (s *stack) BindDomain(ctx context.Context, binding edge.DomainBinding) error {
	if binding.Hostname == "" {
		return errors.New("binding a domain to a Cloudflare stack needs a hostname")
	}
	accountID, script, err := s.soleEntryWorker("bind")
	if err != nil {
		return err
	}
	zoneID, zoneName, err := s.p.resolveZone(ctx, accountID, routeBaseDomain(binding.Hostname))
	if err != nil {
		return err
	}
	if err := s.p.requireTLSCover(ctx, zoneID, zoneName, binding.Hostname); err != nil {
		return err
	}
	if err := s.p.ensureRoute(ctx, s.p.routeSnapshot(), zoneID, routePattern(binding.Hostname), script); err != nil {
		return err
	}
	if err := s.p.bindProxiedPlaceholder(ctx, zoneID, binding.Hostname); err != nil {
		return err
	}
	s.state = edge.RecordBoundDomain(s.state, binding.Hostname)
	return nil
}

func (s *stack) UnbindDomain(ctx context.Context, hostname string) error {
	if hostname == "" {
		return errors.New("unbinding a domain from a Cloudflare stack needs a hostname")
	}
	accountID, scripts, err := s.entryWorkers("unbind")
	if err != nil {
		return err
	}
	zoneID, _, err := s.p.resolveZone(ctx, accountID, routeBaseDomain(hostname))
	if err != nil {
		return err
	}
	if err := s.p.detachRoute(ctx, zoneID, routePattern(hostname), scripts); err != nil {
		return err
	}
	if err := s.p.deleteProxiedRecord(ctx, zoneID, hostname); err != nil {
		return err
	}
	s.state = edge.ForgetBoundDomain(s.state, hostname)
	return nil
}

func joinEntryWorkers(names []string) string {
	return strings.Join(names, ",")
}

func splitEntryWorkers(recorded string) []string {
	if recorded == "" {
		return nil
	}
	return strings.Split(recorded, ",")
}

func (s *stack) entryWorkers(verb string) (accountID string, scriptNames []string, err error) {
	accountID = os.Getenv(envAccountID)
	if accountID == "" {
		return "", nil, fmt.Errorf("%s is not set; it is required to %s a domain on the Cloudflare edge", envAccountID, verb)
	}
	scriptNames = splitEntryWorkers(s.state[stackKeyEntryWorker])
	if len(scriptNames) == 0 {
		return "", nil, fmt.Errorf("stack %q records no entry worker to %s a domain on — deploy it first", s.state[edge.StackKeySlug], verb)
	}
	return accountID, scriptNames, nil
}

func (s *stack) soleEntryWorker(verb string) (accountID, scriptName string, err error) {
	accountID, scriptNames, err := s.entryWorkers(verb)
	if err != nil {
		return "", "", err
	}
	if len(scriptNames) > 1 {
		return "", "", fmt.Errorf("stack %q serves %d apps (%s), and a domain binding names no app, so nothing tells which worker should answer for it — %s a domain on a single-app stack", s.state[edge.StackKeySlug], len(scriptNames), strings.Join(scriptNames, ", "), verb)
	}
	if scriptNames[0] == previewEntryScript {
		return "", "", fmt.Errorf("stack %q is served by the shared preview entry worker %q, which answers for every project, so no single project's domain may be bound to it", s.state[edge.StackKeySlug], previewEntryScript)
	}
	return accountID, scriptNames[0], nil
}

func (p *provider) detachRoute(ctx context.Context, zoneID, pattern string, scriptNames []string) error {
	snap := p.routeSnapshot()
	inZone, err := snap.inZone(ctx, zoneID)
	if err != nil {
		return err
	}
	for _, route := range slices.Clone(inZone) {
		if route.Pattern != pattern || !slices.Contains(scriptNames, route.Script) {
			continue
		}
		if _, err := p.client.Workers.Routes.Delete(ctx, route.ID, workers.RouteDeleteParams{ZoneID: cf.F(zoneID)}); err != nil {
			return fmt.Errorf("delete worker route %q: %w", pattern, err)
		}
		snap.detached(zoneID, route.ID)
	}
	return nil
}

func (p *provider) bindProxiedPlaceholder(ctx context.Context, zoneID, hostname string) error {
	haveAddress, haveProxied, err := p.addressRecordsAt(ctx, zoneID, hostname)
	if err != nil {
		return err
	}
	switch {
	case !haveAddress:
		return p.plantProxiedRecord(ctx, zoneID, hostname)
	case !haveProxied:
		return fmt.Errorf("the DNS record for %q is not proxied through Cloudflare, so no worker route under it can ever fire — set %q to proxied (orange cloud) and bind it again", hostname, hostname)
	}
	return nil
}

func (p *provider) requireTLSCover(ctx context.Context, zoneID, zoneName, hostname string) error {
	if coveredByUniversalSSL(hostname, zoneName) {
		return nil
	}
	covered, err := p.certificatePackCovers(ctx, zoneID, hostname)
	if err != nil {
		return err
	}
	if covered {
		return nil
	}
	return fmt.Errorf("%s is more than one label below %s, which the zone's Universal SSL certificate does not cover, and no active certificate pack covers it either — add a Cloudflare Advanced Certificate for %s and bind it again", hostname, zoneName, hostname)
}

func (p *provider) certificatePackCovers(ctx context.Context, zoneID, hostname string) (bool, error) {
	packs := p.client.SSL.CertificatePacks.ListAutoPaging(ctx, ssl.CertificatePackListParams{ZoneID: cf.F(zoneID)})
	for packs.Next() {
		hosts := activeCertificatePackHosts(packs.Current())
		if slices.ContainsFunc(hosts, func(covered string) bool { return certificateCovers(covered, hostname) }) {
			return true, nil
		}
	}
	if err := packs.Err(); err != nil {
		return false, fmt.Errorf("list certificate packs in zone %s: %w", zoneID, err)
	}
	return false, nil
}

func activeCertificatePackHosts(pack ssl.CertificatePackListResponse) []string {
	raw, err := json.Marshal(pack)
	if err != nil {
		return nil
	}
	var decoded struct {
		Hosts  []string `json:"hosts"`
		Status string   `json:"status"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil
	}
	if decoded.Status != string(ssl.StatusActive) {
		return nil
	}
	return decoded.Hosts
}

func certificateCovers(covered, hostname string) bool {
	if covered == hostname {
		return true
	}
	under, wildcard := strings.CutPrefix(covered, "*.")
	if !wildcard {
		return false
	}
	label, ok := strings.CutSuffix(hostname, "."+under)
	return ok && label != "" && !strings.Contains(label, ".")
}
