package providerserver

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/router"
)

func (r *deployRun) listOriginPreviews() ([]ConfiguredHost, error) {
	if r.spec.Tier != environment.TierPreview || r.previewOn == "" {
		return nil, nil
	}
	var hosts []ConfiguredHost
	for _, entry := range r.spec.Apps {
		kind := r.appRouters[entry.App]
		if kind == r.edgeKind {
			continue
		}
		origin, err := r.findRouterOrigin(kind)
		if err != nil {
			return nil, err
		}
		if origin == nil {
			continue
		}
		if host := findAppHost(r.aliases, entry.App); host.Hostname != "" {
			hosts = append(hosts, ConfiguredHost{Hostname: host.Hostname, App: entry.App, Pointer: router.ResolvePointer(r.spec.Pointer)})
		}
		if host := findAppHost(r.deployment, entry.App); host.Hostname != "" {
			hosts = append(hosts, ConfiguredHost{Hostname: host.Hostname, App: entry.App, Pointer: router.FormatDeploymentPointer(r.spec.Pointer, r.spec.PromotionID)})
		}
	}
	return append(hosts, r.listKeptDeploymentHosts(hosts)...), nil
}

func (r *deployRun) listKeptDeploymentHosts(listed []ConfiguredHost) []ConfiguredHost {
	var kept []ConfiguredHost
	for _, hostname := range r.state.Hostnames() {
		held := r.state.Host(hostname)
		preview, _, deployment := router.ParseDeploymentPointer(held.Pointer)
		if !deployment || preview != router.ResolvePointer(r.spec.Pointer) || held.Edge != r.front.Kind() {
			continue
		}
		if slices.ContainsFunc(listed, func(host ConfiguredHost) bool { return host.Hostname == hostname }) {
			continue
		}
		if kind := r.appRouters[held.App]; kind == "" || kind == r.edgeKind {
			continue
		}
		kept = append(kept, ConfiguredHost{Hostname: hostname, App: held.App, Pointer: held.Pointer})
	}
	return kept
}

func (r *deployRun) forwardAppPreviews(ctx context.Context, progress progress.Log) error {
	hosts, err := r.listOriginPreviews()
	if err != nil || len(hosts) == 0 {
		return err
	}
	if err := r.installPreviewCutover(); err != nil {
		return err
	}
	forwarding := &hostnames{edgeSession: r.edgeSession}
	certified := map[router.Kind]hostCertificates{}
	for _, target := range hosts {
		kind := r.appRouters[target.App]
		certifying, done := certified[kind]
		if !done {
			if certifying, err = r.certifyPreviewWildcard(ctx, forwarding, kind, progress); err != nil {
				return err
			}
			certified[kind] = certifying
		}
		if err := r.forwardAppPreview(ctx, forwarding, target, certifying.hostState.Certificate, progress); err != nil {
			return err
		}
	}
	var errs []error
	for _, certifying := range certified {
		errs = append(errs, certifying.discardSuperseded(ctx, progress))
	}
	return errors.Join(errs...)
}

func (r *deployRun) forwardAppPreview(ctx context.Context, forwarding *hostnames, target ConfiguredHost, wildcard provider.Certificate, progress progress.Log) error {
	hostState := r.state.Host(target.Hostname)
	rotated := hostState.Certificate.ID != wildcard.ID
	hostState.Certificate = provider.Certificate{ID: wildcard.ID, Requested: wildcard.Requested && r.hostingMode() != hostingGlobalPreview}
	hostState.Router = r.appRouters[target.App]
	if slices.Contains(r.edgeStack().State().Bound, target.Hostname) && !rotated {
		_, err := forwarding.refreshOriginClaim(ctx, target, &hostState, progress)
		return err
	}
	progress.Say(fmt.Sprintf("Forwarding %s to the origin that answers %s", target.Hostname, target.App))
	return forwarding.bindOrigin(ctx, target, &hostState, progress)
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

func (r *deployRun) certifyPreviewWildcard(ctx context.Context, forwarding *hostnames, answering router.Kind, progress progress.Log) (hostCertificates, error) {
	wildcard := edge.PreviewWildcard(r.previewOn)
	wildcardState := r.state.Host(wildcard)
	wildcardState.Router = answering
	certifying := forwarding.hostCertificates(wildcard, &wildcardState, answering)
	if err := certifying.certify(ctx, wildcard, progress); err != nil {
		return hostCertificates{}, err
	}
	return certifying, nil
}
