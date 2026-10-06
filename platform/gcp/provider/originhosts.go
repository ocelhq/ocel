package gcp

import (
	"context"
	"errors"
	"slices"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/alb"
)

const maxDNSLabel = 63

type originRouting interface {
	RouteOriginHost(ctx context.Context, host alb.OriginHost) error
	UnrouteOriginHosts(ctx context.Context, tier environment.Tier, service, revision string) error
}

func (p *Provider) originRouting() originRouting {
	if p.originRoutes != nil {
		return p.originRoutes
	}
	return p.edges().openALB().Shielded()
}

func originHostname(release, service, base string) string {
	label := naming.Fit(maxDNSLabel, naming.WordSeparator, naming.Fixed(release), naming.Compressible(service))
	return label + "." + base
}

func (p *Provider) readOriginBase(ctx context.Context, spec provider.StackSpec) (string, error) {
	if !factsOf(spec.Edge).RunsCode || spec.App == nil || len(spec.App.Functions) == 0 {
		return "", nil
	}
	tier := spec.Ref.Tier
	_, recorded, err := originWildcards{keyValues: p.KeyValues()}.read(ctx, tier)
	if err != nil {
		return "", err
	}
	switch {
	case recorded.BaseDomain == "":
		return "", refusal.Refuse(refusal.CodeInvalid,
			"app %s runs as serverless functions behind the Cloudflare worker, which reaches each deployment on its own DNS-only hostname under an origin domain, and tier %s has none: "+
				"add \"edge\": {\"cloudflare\": {\"originDomain\": \"<a domain in your Cloudflare zone>\"}} to your config, then deploy again",
			spec.App.App, tier)
	case recorded.Address == "":
		return "", refusal.Refuse(refusal.CodeNotReady,
			"the origin wildcard *.%s of tier %s has no address yet, because its certificate is still being issued: deploy again once it is issued",
			recorded.BaseDomain, tier)
	}
	return recorded.BaseDomain, nil
}

func (p *Provider) routeOriginHost(
	ctx context.Context,
	spec provider.StackSpec,
	base, service, tag, revision string,
	runProgress progress.Log,
) (string, error) {
	hostname := originHostname(spec.Ref.Name.Release.String(), service, base)
	ensureProgress(runProgress).Say("Routing " + hostname + " to revision tag " + tag + " of Cloud Run service " + service)
	err := p.originRouting().RouteOriginHost(ctx, alb.OriginHost{
		Tier: spec.Ref.Tier, Slug: spec.Ref.Project, Hostname: hostname, Service: service, Tag: tag, Revision: revision,
	})
	if err != nil {
		return "", err
	}
	return "https://" + hostname, nil
}

func (p *Provider) unrouteServiceOriginHosts(ctx context.Context, tier environment.Tier, functions []provider.Function) error {
	var errs []error
	for _, service := range slices.Compact(slices.Sorted(slices.Values(functionServices(functions)))) {
		errs = append(errs, p.originRouting().UnrouteOriginHosts(ctx, tier, service, ""))
	}
	return errors.Join(errs...)
}

func (p *Provider) unrouteRevisionOriginHosts(ctx context.Context, tier environment.Tier, functions []provider.Function) error {
	var errs []error
	for _, function := range functions {
		if function.Physical == "" || function.Revision == "" {
			continue
		}
		errs = append(errs, p.originRouting().UnrouteOriginHosts(ctx, tier, function.Physical, function.Revision))
	}
	return errors.Join(errs...)
}

func functionServices(functions []provider.Function) []string {
	services := make([]string, 0, len(functions))
	for _, function := range functions {
		if function.Physical != "" {
			services = append(services, function.Physical)
		}
	}
	return services
}
