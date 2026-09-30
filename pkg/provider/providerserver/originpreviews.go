package providerserver

import (
	"context"
	"fmt"
	"slices"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/router"
)

func (r *deployRun) listOriginPreviews() []ConfiguredHost {
	if r.spec.Tier != environment.TierPreview || r.previewOn == "" {
		return nil
	}
	var hosts []ConfiguredHost
	site, names := r.previewSite(), r.appNames()
	for slot, entry := range r.spec.Apps {
		kind := r.readAppRouter(entry.App)
		if r.routerOf(kind).Kind() == r.router.Kind() || r.routerOrigin(kind) == nil {
			continue
		}
		if host := site.Host(r.spec.Pointer, edge.AppAt(names, slot)); host != "" {
			hosts = append(hosts, ConfiguredHost{Hostname: host, App: entry.App, Pointer: router.ResolvePointer(r.spec.Pointer)})
		}
	}
	return hosts
}

func (r *deployRun) forwardAppPreviews(ctx context.Context, progress progress.Log) error {
	hosts := r.listOriginPreviews()
	if len(hosts) == 0 {
		return nil
	}
	if err := r.installPreviewCutover(); err != nil {
		return err
	}
	forwarding := &hostnames{edgeSession: r.edgeSession}
	for _, target := range hosts {
		certificate, err := r.certifyPreviewWildcard(ctx, forwarding, r.readAppRouter(target.App), progress)
		if err != nil {
			return err
		}
		hostState := r.state.Host(target.Hostname)
		hostState.Certificate = provider.Certificate{ID: certificate.ID}
		if slices.Contains(r.edgeStack().State().Bound, target.Hostname) {
			if _, err := forwarding.refreshOriginClaim(ctx, target, &hostState, progress); err != nil {
				return err
			}
			continue
		}
		progress.Say(fmt.Sprintf("Forwarding %s to the origin that answers %s", target.Hostname, target.App))
		if err := forwarding.bindOrigin(ctx, target, &hostState, progress); err != nil {
			return err
		}
	}
	return nil
}

func (r *deployRun) installPreviewCutover() error {
	writer, err := dnsFor(r.provider, r.front, r.selection)
	if err != nil {
		return err
	}
	r.installDNSCutover(writer, r.selection.GetDns().GetZone())
	r.cutover.manual = failOnManualRecords(r.sender, r.spans.Promotion)
	return nil
}

func (r *deployRun) certifyPreviewWildcard(ctx context.Context, forwarding *hostnames, answering router.Kind, progress progress.Log) (provider.Certificate, error) {
	wildcard := edge.PreviewWildcard(r.previewOn)
	wildcardState := r.state.Host(wildcard)
	certifying := forwarding.hostCertificates(wildcard, &wildcardState, answering)
	if err := certifying.certify(ctx, wildcard, progress); err != nil {
		return provider.Certificate{}, err
	}
	if err := certifying.discardSuperseded(ctx, progress); err != nil {
		return provider.Certificate{}, err
	}
	return wildcardState.Certificate, nil
}
