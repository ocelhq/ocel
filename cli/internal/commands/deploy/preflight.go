package deploy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/ocelhq/ocel/cli/internal/appurl"
	"github.com/ocelhq/ocel/cli/internal/commands"
	"github.com/ocelhq/ocel/cli/internal/consent"
	"github.com/ocelhq/ocel/cli/internal/executables"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/providerprocess"
	"github.com/ocelhq/ocel/cli/internal/readiness"
	"github.com/ocelhq/ocel/cli/internal/run"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

type preflightFacts struct {
	declined       bool
	project        *project.Project
	containerArchs map[string]string
	urls           map[string]string
}

func previewOpenOptions(dry bool, cfg *project.Project) commands.OpenOptions {
	return commands.OpenOptions{
		Pinning: executables.ChoosePinning(dry),
		Tier:    environmentv1.Tier_TIER_PREVIEW,
		Require: readiness.Infrastructure,
		Slug:    cfg.Slug,
		Domains: cfg.HostnameNames(environmentv1.Tier_TIER_PREVIEW),
	}
}

func preflightPreviewUp(ctx context.Context, dependencies Dependencies, policy consent.Policy, check *run.Span, provider *providerprocess.Provider, cfg *project.Project, resp *contractv1.PreflightResponse, prebuilt bool, pointer string, out io.Writer, in io.Reader) (preflightFacts, error) {
	resolved, archs, err := resolveContainers(ctx, dependencies, check, provider, cfg, resp, prebuilt)
	if err != nil {
		return preflightFacts{}, err
	}
	if err := refuseClaimedDomains(resp.GetDomainClaims(), filepath.Base(cfg.Path), check.Warn); err != nil {
		return preflightFacts{}, err
	}
	if err := ensureBootstrap(ctx, policy, check, provider, cfg, resp.GetBootstrap(), environmentv1.Tier_TIER_PREVIEW, out, in); err != nil {
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

func productionOpenOptions(dry, interactive bool, cfg *project.Project) commands.OpenOptions {
	domains := cfg.HostnameNames(environmentv1.Tier_TIER_PRODUCTION)
	return commands.OpenOptions{
		Pinning: executables.ChoosePinning(dry),
		Tier:    environmentv1.Tier_TIER_PRODUCTION,
		Require: readiness.Infrastructure,
		Slug:    slugToScopeBy(interactive, domains, cfg),
		Domains: domains,
	}
}

func preflightDeploy(ctx context.Context, dependencies Dependencies, policy consent.Policy, check *run.Span, provider *providerprocess.Provider, cfg *project.Project, resp *contractv1.PreflightResponse, prebuilt bool, out io.Writer, in io.Reader) (preflightFacts, error) {
	resolved, archs, err := resolveContainers(ctx, dependencies, check, provider, cfg, resp, prebuilt)
	if err != nil {
		return preflightFacts{}, err
	}
	if err := refuseClaimedDomains(resp.GetDomainClaims(), filepath.Base(cfg.Path), check.Warn); err != nil {
		return preflightFacts{}, err
	}
	if err := ensureBootstrap(ctx, policy, check, provider, cfg, resp.GetBootstrap(), environmentv1.Tier_TIER_PRODUCTION, out, in); err != nil {
		return preflightFacts{}, err
	}
	proceed, err := guardNewProject(ctx, policy, check, cfg, resp.GetKnownSlugs())
	if err != nil {
		return preflightFacts{}, err
	}
	return preflightFacts{declined: !proceed, project: resolved, containerArchs: archs, urls: appurl.Production(resolved)}, nil
}

func resolveContainers(ctx context.Context, dependencies Dependencies, check *run.Span, provider *providerprocess.Provider, cfg *project.Project, resp *contractv1.PreflightResponse, prebuilt bool) (*project.Project, map[string]string, error) {
	resolved, err := cfg.ResolveComputes(resp.GetComputes(), provider.Name())
	if err != nil {
		return nil, nil, err
	}
	if err := requireProjectRegistryPassword(resolved); err != nil {
		return nil, nil, err
	}
	archs, err := readiness.ReadContainerArchs(ctx, provider, resolved, resp.GetContainerArchs())
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

func ensureBootstrap(ctx context.Context, policy consent.Policy, check *run.Span, provider *providerprocess.Provider, cfg *project.Project, status *contractv1.BootstrapStatus, tier environmentv1.Tier, out io.Writer, in io.Reader) error {
	if policy.DryRun {
		return readiness.NewGap(status).RefuseIncomplete(tier)
	}
	return readiness.OfferRepair(ctx, check, provider, readiness.NewGap(status), tier, cfg.EdgeSelection(), policy.Interactive, out, in)
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
