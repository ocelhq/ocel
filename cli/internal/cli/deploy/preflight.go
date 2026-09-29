package deploy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/ocelhq/ocel/cli/internal/appregistry"
	"github.com/ocelhq/ocel/cli/internal/appurl"
	"github.com/ocelhq/ocel/cli/internal/cli/bootstrap"
	"github.com/ocelhq/ocel/cli/internal/cli/cmddeps"
	"github.com/ocelhq/ocel/cli/internal/cli/preflight"
	"github.com/ocelhq/ocel/cli/internal/consent"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/internal/providerclient"
	"github.com/ocelhq/ocel/cli/internal/run"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

func preflightPreview(ctx context.Context, check *run.Span, prov *providerclient.Provider, cfg *projectconfig.Config) error {
	return bootstrap.Ready(ctx, check, prov, cfg, environmentv1.Tier_TIER_PREVIEW, "ocel bootstrap preview")
}

type preflightFacts struct {
	declined       bool
	compute        string
	containerArchs map[string]string
	urls           map[string]string
}

func preflightPreviewUp(ctx context.Context, deps cmddeps.Deps, policy consent.Policy, check *run.Span, prov *providerclient.Provider, cfg *projectconfig.Config, pointer string, out io.Writer, in io.Reader) (preflightFacts, error) {
	resp, err := preflight.Run(ctx, check, prov, cfg, environmentv1.Tier_TIER_PREVIEW, cfg.Slug, preflight.Names(preflight.Hostnames(cfg, "preview")), preflight.Frameworks(cfg), "ocel bootstrap preview")
	if err != nil {
		return preflightFacts{}, err
	}
	compute, err := preflight.ResolveComputes(cfg, resp.GetComputes(), prov.Name())
	if err != nil {
		return preflightFacts{}, err
	}
	if err := appregistry.RequireSecret(cfg); err != nil {
		return preflightFacts{}, err
	}
	if err := deps.RefuseUnbuildableImages(ctx, check, cfg, resp.GetContainerArchs()); err != nil {
		return preflightFacts{}, err
	}
	if err := refuseClaimedDomains(resp.GetDomainClaims(), filepath.Base(cfg.Path), check.Warn); err != nil {
		return preflightFacts{}, err
	}
	if err := ensureBootstrap(ctx, policy, check, prov, cfg, resp.GetBootstrap(), environmentv1.Tier_TIER_PREVIEW, out, in); err != nil {
		return preflightFacts{}, err
	}
	site, err := requirePreviewDomain(cfg, resp.GetPreviewWildcard(), resp.GetIdentity(), pointer, check)
	if err != nil {
		return preflightFacts{}, err
	}
	proceed, err := guardNewProject(ctx, policy, check, cfg, resp.GetKnownSlugs())
	if err != nil {
		return preflightFacts{}, err
	}
	return preflightFacts{
		declined:       !proceed,
		compute:        compute,
		containerArchs: resp.GetContainerArchs(),
		urls: appurl.Preview(cfg, func(app string) string {
			return site.Host(pointer, app)
		}),
	}, nil
}

func preflightDeploy(ctx context.Context, deps cmddeps.Deps, policy consent.Policy, check *run.Span, prov *providerclient.Provider, cfg *projectconfig.Config, out io.Writer, in io.Reader) (preflightFacts, error) {
	domains := preflight.Names(preflight.Hostnames(cfg, "production"))
	resp, err := preflight.Run(ctx, check, prov, cfg, environmentv1.Tier_TIER_PRODUCTION, slugToScopeBy(policy.Interactive, domains, cfg), domains, preflight.Frameworks(cfg), "ocel bootstrap production")
	if err != nil {
		return preflightFacts{}, err
	}
	compute, err := preflight.ResolveComputes(cfg, resp.GetComputes(), prov.Name())
	if err != nil {
		return preflightFacts{}, err
	}
	if err := appregistry.RequireSecret(cfg); err != nil {
		return preflightFacts{}, err
	}
	if err := deps.RefuseUnbuildableImages(ctx, check, cfg, resp.GetContainerArchs()); err != nil {
		return preflightFacts{}, err
	}
	if err := refuseClaimedDomains(resp.GetDomainClaims(), filepath.Base(cfg.Path), check.Warn); err != nil {
		return preflightFacts{}, err
	}
	if err := ensureBootstrap(ctx, policy, check, prov, cfg, resp.GetBootstrap(), environmentv1.Tier_TIER_PRODUCTION, out, in); err != nil {
		return preflightFacts{}, err
	}
	proceed, err := guardNewProject(ctx, policy, check, cfg, resp.GetKnownSlugs())
	if err != nil {
		return preflightFacts{}, err
	}
	return preflightFacts{declined: !proceed, compute: compute, containerArchs: resp.GetContainerArchs(), urls: appurl.Production(cfg)}, nil
}

func ensureBootstrap(ctx context.Context, policy consent.Policy, check *run.Span, prov *providerclient.Provider, cfg *projectconfig.Config, status *contractv1.BootstrapStatus, tier environmentv1.Tier, out io.Writer, in io.Reader) error {
	if policy.DryRun {
		return bootstrap.PlanFor(status).Insist(tier)
	}
	return bootstrap.Offer(ctx, check, prov, status, tier, cfg.EdgeSelection(), policy.Interactive, out, in)
}

func slugToScopeBy(interactive bool, domains []string, cfg *projectconfig.Config) string {
	if interactive || len(domains) > 0 {
		return cfg.Slug
	}
	return ""
}

func guardNewProject(ctx context.Context, policy consent.Policy, check *run.Span, cfg *projectconfig.Config, knownSlugs []string) (bool, error) {
	if len(knownSlugs) == 0 {
		return true, nil
	}
	check.Warn(fmt.Sprintf("No existing deployment for slug %q.\nThis will create a NEW project.\nThis backend already has: %s",
		cfg.Slug, strings.Join(knownSlugs, ", ")))
	return policy.Confirm(ctx, check, "Continue?")
}

func refuseClaimedDomains(claims []*contractv1.DomainClaim, configName string, warn func(string)) error {
	var b strings.Builder
	for _, claim := range claims {
		if cause := claim.GetCause(); cause != "" {
			warn(fmt.Sprintf("Could not read who serves %s: %s\n  → this deploy continues; if another project owns that hostname, this run takes it over",
				claim.GetHostname(), cause))
			continue
		}
		if claim.GetStatus() != contractv1.DomainClaim_STATUS_CLAIMED {
			continue
		}
		if b.Len() == 0 {
			b.WriteString("another project already serves a hostname this project declares:")
		}
		fmt.Fprintf(&b, "\n  ✗ %s is owned by %s", claim.GetHostname(), claim.GetOwner())
	}
	if b.Len() == 0 {
		return nil
	}
	b.WriteString("\n    → a hostname belongs to one project, so deploying would take it over: remove it from this project's " +
		configName + ", or tear the owning project down (`ocel destroy production` / `ocel destroy preview` in it), then deploy again")
	return errors.New(b.String())
}
