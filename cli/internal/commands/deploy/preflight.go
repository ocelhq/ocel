package deploy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/ocelhq/ocel/cli/internal/appurl"
	"github.com/ocelhq/ocel/cli/internal/commands/bootstrap"
	"github.com/ocelhq/ocel/cli/internal/consent"
	"github.com/ocelhq/ocel/cli/internal/preflight"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/providerprocess"
	"github.com/ocelhq/ocel/cli/internal/run"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

func preflightPreview(ctx context.Context, check *run.Span, prov *providerprocess.Provider, cfg *project.Project) error {
	return bootstrap.Ready(ctx, check, prov, cfg, environmentv1.Tier_TIER_PREVIEW, "ocel bootstrap preview")
}

type preflightFacts struct {
	declined       bool
	project        *project.Project
	containerArchs map[string]string
	urls           map[string]string
}

func preflightPreviewUp(ctx context.Context, dependencies Dependencies, policy consent.Policy, check *run.Span, prov *providerprocess.Provider, cfg *project.Project, prebuilt bool, pointer string, out io.Writer, in io.Reader) (preflightFacts, error) {
	resp, err := preflight.Run(ctx, check, prov, cfg, environmentv1.Tier_TIER_PREVIEW, cfg.Slug, preflight.Names(preflight.Hostnames(cfg, environmentv1.Tier_TIER_PREVIEW)), preflight.Frameworks(cfg), "ocel bootstrap preview")
	if err != nil {
		return preflightFacts{}, err
	}
	resolved, archs, err := resolveContainers(ctx, dependencies, check, prov, cfg, resp, prebuilt)
	if err != nil {
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
		project:        resolved,
		containerArchs: archs,
		urls: appurl.Preview(resolved, func(app string) string {
			return site.Host(pointer, app)
		}),
	}, nil
}

func preflightDeploy(ctx context.Context, dependencies Dependencies, policy consent.Policy, check *run.Span, prov *providerprocess.Provider, cfg *project.Project, prebuilt bool, out io.Writer, in io.Reader) (preflightFacts, error) {
	domains := preflight.Names(preflight.Hostnames(cfg, environmentv1.Tier_TIER_PRODUCTION))
	resp, err := preflight.Run(ctx, check, prov, cfg, environmentv1.Tier_TIER_PRODUCTION, slugToScopeBy(policy.Interactive, domains, cfg), domains, preflight.Frameworks(cfg), "ocel bootstrap production")
	if err != nil {
		return preflightFacts{}, err
	}
	resolved, archs, err := resolveContainers(ctx, dependencies, check, prov, cfg, resp, prebuilt)
	if err != nil {
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
	return preflightFacts{declined: !proceed, project: resolved, containerArchs: archs, urls: appurl.Production(resolved)}, nil
}

func resolveContainers(ctx context.Context, dependencies Dependencies, check *run.Span, prov *providerprocess.Provider, cfg *project.Project, resp *contractv1.PreflightResponse, prebuilt bool) (*project.Project, map[string]string, error) {
	resolved, err := cfg.ResolveComputes(resp.GetComputes(), prov.Name())
	if err != nil {
		return nil, nil, err
	}
	if err := requireProjectRegistryPassword(resolved); err != nil {
		return nil, nil, err
	}
	archs, err := preflight.ContainerArchs(ctx, prov, resolved, resp.GetContainerArchs())
	if err != nil {
		return nil, nil, err
	}
	if prebuilt {
		return resolved, archs, nil
	}
	if err := dependencies.RefuseUnbuildableImages(ctx, check, resolved, archs); err != nil {
		return nil, nil, err
	}
	return resolved, archs, nil
}

func ensureBootstrap(ctx context.Context, policy consent.Policy, check *run.Span, prov *providerprocess.Provider, cfg *project.Project, status *contractv1.BootstrapStatus, tier environmentv1.Tier, out io.Writer, in io.Reader) error {
	if policy.DryRun {
		return bootstrap.PlanFor(status).Insist(tier)
	}
	return bootstrap.Offer(ctx, check, prov, status, tier, cfg.EdgeSelection(), policy.Interactive, out, in)
}

func slugToScopeBy(interactive bool, domains []string, cfg *project.Project) string {
	if interactive || len(domains) > 0 {
		return cfg.Slug
	}
	return ""
}

func guardNewProject(ctx context.Context, policy consent.Policy, check *run.Span, cfg *project.Project, knownSlugs []string) (bool, error) {
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
