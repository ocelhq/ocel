package deploy

import (
	"context"
	"errors"
	"fmt"
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
	"github.com/ocelhq/ocel/pkg/edge"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	"github.com/ocelhq/ocel/pkg/provider"
)

type preflightFacts struct {
	declined       bool
	project        *project.Project
	containerArchs map[string]string
	workerCeilings []provider.WorkerCeiling
	urls           map[string]string
	mintedAlias    string
	builtAlias     string
}

func previewOpenOptions(policy consent.Policy, cfg *project.Project) commands.OpenOptions {
	return commands.OpenOptions{
		Pinning:       executables.ChoosePinning(policy.DryRun),
		Tier:          environmentv1.Tier_TIER_PREVIEW,
		Require:       readiness.Features,
		Slug:          cfg.Slug,
		ClaimsDomains: true,
		Policy:        policy,
	}
}

func preflightPreviewUp(ctx context.Context, dependencies Dependencies, policy consent.Policy, check *run.Span, process *providerprocess.Provider, cfg *project.Project, read readiness.Preflight, prebuilt bool, env *environmentv1.Environment) (preflightFacts, error) {
	resp := read.Response
	resolved, archs, err := resolveContainers(ctx, dependencies, check, read, prebuilt)
	if err != nil {
		return preflightFacts{}, err
	}
	if err := refuseClaimedDomains(resp.GetDomainClaims(), filepath.Base(cfg.Path), check.Warn); err != nil {
		return preflightFacts{}, err
	}
	if err := refuseStaleBootstrap(policy, check, resp.GetBootstrap(), environmentv1.Tier_TIER_PREVIEW); err != nil {
		return preflightFacts{}, err
	}
	if err := refuseMissingPreviewDomain(cfg, resp.GetPreviewWildcard(), resp.GetIdentity(), check); err != nil {
		return preflightFacts{}, err
	}
	proceed, err := guardNewProject(ctx, policy, check, cfg, resp.GetKnownSlugs())
	if err != nil {
		return preflightFacts{}, err
	}
	if !proceed {
		return preflightFacts{declined: true}, nil
	}
	token, err := edge.NewPreviewToken()
	if err != nil {
		return preflightFacts{}, err
	}
	ensured, err := ensurePreviewAlias(ctx, process, resolved, env, token, policy.DryRun)
	if err != nil {
		return preflightFacts{}, err
	}
	facts := preflightFacts{
		project:        resolved,
		containerArchs: archs,
		workerCeilings: provider.WorkerCeilingsOf(resp.GetWorkerCeilings()),
		urls:           appurl.FormatPreviewURLs(ensured.GetHostnames()),
		builtAlias:     ensured.GetToken(),
	}
	if !policy.DryRun {
		facts.mintedAlias = token
	}
	return facts, nil
}

func ensurePreviewAlias(ctx context.Context, provider *providerprocess.Provider, cfg *project.Project, env *environmentv1.Environment, token string, dry bool) (ensured *contractv1.EnsurePreviewAliasResponse, err error) {
	err = provider.Call(ctx, func(client contractv1connect.ProviderServiceClient) error {
		ensured, err = client.EnsurePreviewAlias(ctx, &contractv1.EnsurePreviewAliasRequest{
			Slug:        cfg.Slug,
			Environment: env,
			Token:       token,
			Apps:        previewAppNames(cfg),
			Domains:     cfg.HostnameNames(environmentv1.Tier_TIER_PREVIEW),
			Dry:         dry,
		})
		return err
	})
	return ensured, err
}

func forgetUnclaimedPreviewAlias(ctx context.Context, provider *providerprocess.Provider, slug string, env *environmentv1.Environment, token string) error {
	if token == "" {
		return nil
	}
	return provider.Call(context.WithoutCancel(ctx), func(client contractv1connect.ProviderServiceClient) error {
		_, err := client.ForgetPreviewAlias(context.WithoutCancel(ctx), &contractv1.ForgetPreviewAliasRequest{Slug: slug, Environment: env, Token: token})
		return err
	})
}

func productionOpenOptions(policy consent.Policy, cfg *project.Project) commands.OpenOptions {
	return commands.OpenOptions{
		Pinning:         executables.ChoosePinning(policy.DryRun),
		Tier:            environmentv1.Tier_TIER_PRODUCTION,
		Require:         readiness.Features,
		Slug:            slugToScopeBy(policy.Interactive, cfg.HostnameNames(environmentv1.Tier_TIER_PRODUCTION), cfg),
		ClaimsDomains:   true,
		RequireHostname: true,
		Policy:          policy,
	}
}

func preflightDeploy(ctx context.Context, dependencies Dependencies, policy consent.Policy, check *run.Span, cfg *project.Project, read readiness.Preflight, prebuilt bool) (preflightFacts, error) {
	resp := read.Response
	resolved, archs, err := resolveContainers(ctx, dependencies, check, read, prebuilt)
	if err != nil {
		return preflightFacts{}, err
	}
	if err := refuseClaimedDomains(resp.GetDomainClaims(), filepath.Base(cfg.Path), check.Warn); err != nil {
		return preflightFacts{}, err
	}
	if err := refuseStaleBootstrap(policy, check, resp.GetBootstrap(), environmentv1.Tier_TIER_PRODUCTION); err != nil {
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
		workerCeilings: provider.WorkerCeilingsOf(resp.GetWorkerCeilings()),
		urls:           appurl.FormatProductionURLs(resolved),
	}, nil
}

func resolveContainers(ctx context.Context, dependencies Dependencies, check *run.Span, read readiness.Preflight, prebuilt bool) (*project.Project, map[string]string, error) {
	resolved, archs := read.Project, read.Response.GetContainerArchs()
	if err := requireProjectRegistryPassword(resolved); err != nil {
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

func refuseStaleBootstrap(policy consent.Policy, check *run.Span, status *contractv1.BootstrapStatus, tier environmentv1.Tier) error {
	gap := readiness.NewGap(status)
	if policy.DryRun {
		return gap.RefuseIncomplete(tier)
	}
	gap.WarnStale(tier, check)
	return nil
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
