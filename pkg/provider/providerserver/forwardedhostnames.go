package providerserver

import (
	"context"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/router"
)

func (h *handlers) refuseUnreadyForwards(ctx context.Context, steps preflightSteps, p provider.Provider, tier environment.Tier, req *contractv1.PreflightRequest, previewWildcard *contractv1.PreviewWildcard) error {
	if !req.GetCheckForwarding() || h.skipChecks {
		return nil
	}
	front, err := h.edgeFor(p, req.GetEdge())
	if err != nil {
		return provider.RefusalError(err)
	}
	certificates := front.Hooks().ClientCertificates
	if certificates == nil || readTunnelToOrigin(front) != edge.None {
		return nil
	}
	forwarded, err := listForwardedHostnames(p, front, tier, req, previewWildcard)
	if err != nil || len(forwarded) == 0 {
		return provider.RefusalError(err)
	}
	err = steps.run(string(front.Kind()), progress.Checking.Title("forwarding to the origin for "+strings.Join(forwarded, ", ")), func() error {
		for _, hostname := range forwarded {
			if err := certificates.RefuseUnready(ctx, hostname); err != nil {
				return err
			}
		}
		return nil
	})
	return provider.RefusalError(err)
}

func listForwardedHostnames(p provider.Provider, front edge.Edge, tier environment.Tier, req *contractv1.PreflightRequest, previewWildcard *contractv1.PreviewWildcard) ([]string, error) {
	if tier == environment.TierPreview {
		return listForwardedPreviewWildcards(p, front, req.GetDomains(), previewWildcard)
	}
	var forwarded []string
	for _, hostname := range req.GetDomains() {
		kind, paired := readDomainRouter(p.Facts(), front.Kind(), req.GetDomainApps()[hostname], req.GetContainers())
		if !paired {
			continue
		}
		opened, err := p.Routers().Open(kind)
		if err != nil {
			return nil, err
		}
		if routerOriginBehind(front, opened) != nil {
			forwarded = append(forwarded, hostname)
		}
	}
	return forwarded, nil
}

func readDomainRouter(facts provider.Facts, front edge.Kind, app string, containers []*contractv1.ContainerApp) (router.Kind, bool) {
	if app == "" {
		return facts.FindEdgeRouter(front)
	}
	compute := provider.ComputeServerless
	if slices.ContainsFunc(containers, func(container *contractv1.ContainerApp) bool { return container.GetApp() == app }) {
		compute = provider.ComputeContainer
	}
	return facts.PairedRouter(front, compute)
}

func listForwardedPreviewWildcards(p provider.Provider, front edge.Edge, declared []string, previewWildcard *contractv1.PreviewWildcard) ([]string, error) {
	if front.Facts().RunsCode {
		return nil, nil
	}
	paired, err := openEdgeRouter(p, front.Kind())
	if err != nil {
		return nil, err
	}
	switch {
	case routerOriginBehind(front, paired) == nil:
		return nil, nil
	case len(declared) > 0:
		return declared, nil
	case previewWildcard.GetBaseDomain() != "":
		return []string{edge.PreviewWildcard(previewWildcard.GetBaseDomain())}, nil
	}
	return nil, nil
}
