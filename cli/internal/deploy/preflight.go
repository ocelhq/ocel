package deploy

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/ocelhq/ocel/cli/internal/appurl"
	"github.com/ocelhq/ocel/cli/internal/consent"
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
	registry       *contractv1.ImageRegistry
}

func runPreflight(ctx context.Context, dependencies Dependencies, policy consent.Policy, check *run.Span, process *providerprocess.Provider, cfg *project.Project, read readiness.Preflight, prebuilt bool, env *environmentv1.Environment) (preflightFacts, error) {
	resp := read.Response
	preview := env.GetTier() == environmentv1.Tier_TIER_PREVIEW
	resolved, archs, err := resolveContainers(ctx, dependencies, check, read, prebuilt)
	if err != nil {
		return preflightFacts{}, err
	}
	if err := refuseClaimedDomains(resp.GetDomainClaims(), filepath.Base(cfg.Path)); err != nil {
		return preflightFacts{}, err
	}
	if err := refuseStaleBootstrap(policy, check, resp.GetBootstrap(), env.GetTier()); err != nil {
		return preflightFacts{}, err
	}
	if preview {
		if err := refuseMissingPreviewDomain(cfg, resp.GetPreviewWildcard(), resp.GetIdentity(), resp.GetHostnameRequired(), check); err != nil {
			return preflightFacts{}, err
		}
	}
	proceed, err := guardNewProject(ctx, policy, check, cfg, resp.GetKnownSlugs())
	if err != nil {
		return preflightFacts{}, err
	}
	if !proceed {
		return preflightFacts{declined: true}, nil
	}
	facts := preflightFacts{
		project:        resolved,
		containerArchs: archs,
		workerCeilings: provider.WorkerCeilingsOf(process.Facts().GetWorkerCeilings()),
		registry:       read.Registry,
	}
	if !preview {
		facts.urls = appurl.FormatProductionURLs(resolved)
		return facts, nil
	}
	if !assignsPreviewAlias(cfg, resp.GetPreviewWildcard(), resp.GetHostnameRequired()) {
		return facts, nil
	}
	token, err := edge.NewPreviewToken()
	if err != nil {
		return preflightFacts{}, err
	}
	ensured, err := ensurePreviewAlias(ctx, process, resolved, env, token, policy.DryRun)
	if err != nil {
		return preflightFacts{}, err
	}
	facts.urls = appurl.FormatPreviewURLs(ensured.GetHostnames())
	facts.builtAlias = ensured.GetToken()
	if !policy.DryRun {
		facts.mintedAlias = token
	}
	return facts, nil
}

func assignsPreviewAlias(cfg *project.Project, wildcard *contractv1.PreviewWildcard, hostnameRequired bool) bool {
	return hostnameRequired || cfg.Domains.Preview != "" || wildcard.GetBaseDomain() != ""
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
	return provider.Call(ctx, func(client contractv1connect.ProviderServiceClient) error {
		_, err := client.ForgetPreviewAlias(ctx, &contractv1.ForgetPreviewAliasRequest{Slug: slug, Environment: env, Token: token})
		return err
	})
}

func resolveContainers(ctx context.Context, dependencies Dependencies, check *run.Span, read readiness.Preflight, prebuilt bool) (*project.Project, map[string]string, error) {
	resolved, archs := read.Project, read.Response.GetContainerArchs()
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
	gap.WarnUnreadable(tier, check)
	if policy.DryRun {
		return gap.RefuseIncomplete(tier)
	}
	gap.WarnStale(tier, check)
	return nil
}

func guardNewProject(ctx context.Context, policy consent.Policy, check *run.Span, cfg *project.Project, knownSlugs []string) (bool, error) {
	if len(knownSlugs) == 0 {
		return true, nil
	}
	check.Warn(fmt.Sprintf("No existing deployment for slug %q.\nThis will create a NEW project.\nThis backend already has: %s",
		cfg.Slug, strings.Join(knownSlugs, ", ")))
	return policy.Confirm(ctx, check, consent.Guard{
		ID:       consent.GuardNewProject,
		Question: "Continue?",
		Action:   fmt.Sprintf("creating new project %q", cfg.Slug),
	})
}

func refuseClaimedDomains(claims []*contractv1.DomainClaim, configName string) error {
	var claimed, unread strings.Builder
	for _, claim := range claims {
		if cause := claim.GetCause(); cause != "" {
			fmt.Fprintf(&unread, "\n  ✗ could not read who serves %s: %s", claim.GetHostname(), cause)
			continue
		}
		if claim.GetStatus() != contractv1.DomainClaim_STATUS_CLAIMED {
			continue
		}
		fmt.Fprintf(&claimed, "\n  ✗ %s is owned by %s", claim.GetHostname(), claim.GetOwner())
	}
	var refusals []string
	if claimed.Len() > 0 {
		refusals = append(refusals, "another project already serves a hostname this project declares:"+claimed.String()+
			"\n    → a hostname belongs to one project, so deploying would take it over: remove it from this project's "+
			configName+", or tear the owning project down (`ocel projects destroy <slug>`), then deploy again")
	}
	if unread.Len() > 0 {
		refusals = append(refusals, "the provider could not say whether another project serves a hostname this project declares:"+unread.String()+
			"\n    → deploying would take the hostname over if another project owns it: deploy again once the provider can read who serves it")
	}
	if len(refusals) == 0 {
		return nil
	}
	return errors.New(strings.Join(refusals, "\n"))
}
