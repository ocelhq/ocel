package cloudflare

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	cf "github.com/cloudflare/cloudflare-go/v4"
	"github.com/cloudflare/cloudflare-go/v4/ssl"
	"github.com/cloudflare/cloudflare-go/v4/workers"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/refusal"
)

func (s *stack) BindDomain(ctx context.Context, binding edge.DomainBinding) error {
	if binding.Hostname == "" {
		return errors.New("binding a domain to a Cloudflare stack needs a hostname")
	}
	if binding.Origin != nil {
		return s.bindForwarded(ctx, binding)
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
	if err := s.p.ensureRoute(ctx, s.p.routeSnapshot(), zoneID, routePattern(binding.Hostname), script, routeSpec{owns: projectOwnsScript(s.p.namespace, s.state.Slug)}); err != nil {
		return err
	}
	if err := s.p.refuseGreyCloud(ctx, zoneID, binding.Hostname); err != nil {
		return err
	}
	s.state.Bind(binding.Hostname)
	return nil
}

func (s *stack) UnbindDomain(ctx context.Context, hostname string) error {
	if hostname == "" {
		return errors.New("unbinding a domain from a Cloudflare stack needs a hostname")
	}
	if s.isForwarded(hostname) {
		return s.unbindForwarded(ctx, hostname)
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
	s.state.Release(hostname)
	return nil
}

func (s *stack) entryWorkers(verb string) (accountID string, scriptNames []string, err error) {
	accountID, err = requireAccountID(fmt.Sprintf("%s a domain on the Cloudflare edge", verb))
	if err != nil {
		return "", nil, err
	}
	scriptNames = s.own.EntryWorkers
	if len(scriptNames) == 0 {
		return "", nil, fmt.Errorf("stack %q records no entry worker to %s a domain on — deploy it first", s.state.Slug, verb)
	}
	return accountID, scriptNames, nil
}

func (s *stack) soleEntryWorker(verb string) (accountID, scriptName string, err error) {
	accountID, scriptNames, err := s.entryWorkers(verb)
	if err != nil {
		return "", "", err
	}
	if len(scriptNames) > 1 {
		return "", "", fmt.Errorf("stack %q serves %d apps (%s), and a domain binding names no app, so nothing tells which worker should answer for it — %s a domain on a single-app stack", s.state.Slug, len(scriptNames), strings.Join(scriptNames, ", "), verb)
	}
	if scriptNames[0] == previewEntryScript {
		return "", "", fmt.Errorf("stack %q is served by the shared preview entry worker %q, which answers for every project, so no single project's domain may be bound to it", s.state.Slug, previewEntryScript)
	}
	return accountID, scriptNames[0], nil
}

func (p *cloudflare) detachRoute(ctx context.Context, zoneID, pattern string, scriptNames []string) error {
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

func (p *cloudflare) refuseGreyCloud(ctx context.Context, zoneID, hostname string) error {
	haveAddress, haveProxied, err := p.addressRecordsAt(ctx, zoneID, hostname)
	if err != nil {
		return err
	}
	if haveAddress && !haveProxied {
		return fmt.Errorf("the DNS record for %q is not proxied through Cloudflare, so no worker route under it can ever fire — set %q to proxied (orange cloud) and bind it again", hostname, hostname)
	}
	return nil
}

func (p *cloudflare) requireTLSCover(ctx context.Context, zoneID, zoneName, hostname string) error {
	if coveredByUniversalSSL(hostname, zoneName) {
		return nil
	}
	covered, err := p.isCoveredByCertificatePack(ctx, zoneID, hostname)
	if err != nil {
		return err
	}
	if covered {
		return nil
	}
	return fmt.Errorf("%s is more than one label below %s, which the zone's Universal SSL certificate does not cover, and no active certificate pack covers it either — add a Cloudflare Advanced Certificate for %s and bind it again", hostname, zoneName, hostname)
}

func (p *cloudflare) isCoveredByCertificatePack(ctx context.Context, zoneID, hostname string) (bool, error) {
	status, err := p.readCoveringPackStatus(ctx, zoneID, hostname)
	return status == string(ssl.StatusActive), err
}

var pendingPackStatuses = []string{
	string(ssl.StatusInitializing), string(ssl.StatusPendingValidation), string(ssl.StatusPendingIssuance), string(ssl.StatusPendingDeployment),
}

func (p *cloudflare) readCoveringPackStatus(ctx context.Context, zoneID, hostname string) (string, error) {
	packs := p.client.SSL.CertificatePacks.ListAutoPaging(ctx, ssl.CertificatePackListParams{ZoneID: cf.F(zoneID)})
	cover := ""
	for packs.Next() {
		status, hosts, err := decodeCertificatePack(packs.Current())
		if err != nil {
			return "", fmt.Errorf("read a certificate pack in zone %s: %w", zoneID, err)
		}
		if !slices.ContainsFunc(hosts, func(covered string) bool { return isCoveredByName(covered, hostname) }) {
			continue
		}
		switch {
		case status == string(ssl.StatusActive):
			return status, nil
		case slices.Contains(pendingPackStatuses, status):
			cover = status
		}
	}
	if err := packs.Err(); err != nil {
		return "", fmt.Errorf("list certificate packs in zone %s: %w", zoneID, err)
	}
	return cover, nil
}

func decodeCertificatePack(pack ssl.CertificatePackListResponse) (string, []string, error) {
	raw, err := json.Marshal(pack)
	if err != nil {
		return "", nil, err
	}
	var decoded struct {
		Hosts  []string `json:"hosts"`
		Status string   `json:"status"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return "", nil, err
	}
	return decoded.Status, decoded.Hosts, nil
}

func isCoveredByName(covered, hostname string) bool {
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

func (p *cloudflare) orderTLSCover(ctx context.Context, zoneID, zoneName, hostname string) error {
	if coveredByUniversalSSL(hostname, zoneName) {
		return nil
	}
	status, err := p.readCoveringPackStatus(ctx, zoneID, hostname)
	switch {
	case err != nil:
		return err
	case status == string(ssl.StatusActive):
		return nil
	case status != "":
		return refusal.Refuse(refusal.CodeNotReady,
			"Cloudflare is still issuing the advanced certificate that covers %s (%s): forward it again once the certificate is active, which takes minutes on a zone Cloudflare's DNS serves",
			hostname, status)
	}
	if _, err := p.client.SSL.CertificatePacks.New(ctx, ssl.CertificatePackNewParams{
		ZoneID:               cf.F(zoneID),
		Type:                 cf.F(ssl.CertificatePackNewParamsTypeAdvanced),
		Hosts:                cf.F([]ssl.HostParam{zoneName, hostname}),
		ValidationMethod:     cf.F(ssl.CertificatePackNewParamsValidationMethodTXT),
		ValidityDays:         cf.F(ssl.CertificatePackNewParamsValidityDays90),
		CertificateAuthority: cf.F(ssl.CertificatePackNewParamsCertificateAuthorityLetsEncrypt),
	}); err != nil {
		return refusal.Refuse(refusal.CodeInvalid,
			"%s is more than one label below zone %s, so the zone's Universal SSL certificate does not cover it, and ordering an advanced certificate that does failed: %v\n"+
				"Add Advanced Certificate Manager to zone %s, or serve it from a domain at a zone apex, whose names one label down Universal SSL covers: previews on `*.<zone>` of a zone of their own",
			hostname, zoneName, err, zoneName)
	}
	return refusal.Refuse(refusal.CodeNotReady,
		"ordered an advanced certificate covering %s from Cloudflare, since zone %s's Universal SSL certificate covers only one label below it: forward it again once the certificate is active, which takes minutes on a zone Cloudflare's DNS serves",
		hostname, zoneName)
}
