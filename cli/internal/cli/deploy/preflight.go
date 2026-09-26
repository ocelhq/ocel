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
	"github.com/ocelhq/ocel/cli/internal/edgewire"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/internal/providerclient"
	"github.com/ocelhq/ocel/cli/internal/runui"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

func preflightPreview(ctx context.Context, ui *runui.Session, runner *providerclient.Runner, cfg *projectconfig.Config) error {
	return bootstrap.Ready(ctx, ui, runner, cfg, environmentv1.Tier_TIER_PREVIEW, "ocel bootstrap preview")
}

type preflightFacts struct {
	knownSlugs     []string
	compute        string
	containerArchs map[string]string
	urls           map[string]string
}

func preflightPreviewUp(ctx context.Context, deps cmddeps.Deps, ui *runui.Session, runner *providerclient.Runner, cfg *projectconfig.Config, pointer string, out io.Writer, in io.Reader) (preflightFacts, error) {
	resp, err := preflight.Run(ctx, ui, runner, cfg, environmentv1.Tier_TIER_PREVIEW, cfg.Slug, preflight.Names(preflight.Hostnames(cfg, "preview")), preflight.Frameworks(cfg), "ocel bootstrap preview")
	if err != nil {
		return preflightFacts{}, err
	}
	compute, err := preflight.ResolveComputes(cfg, resp.GetComputes(), runner.Name())
	if err != nil {
		return preflightFacts{}, err
	}
	if err := appregistry.RequireSecret(cfg); err != nil {
		return preflightFacts{}, err
	}
	if err := deps.RequireImageBuilder(ctx, ui, cfg, resp.GetContainerArchs()); err != nil {
		return preflightFacts{}, err
	}
	if err := refuseClaimedDomains(resp.GetDomainClaims(), filepath.Base(cfg.Path), ui.Warning); err != nil {
		return preflightFacts{}, err
	}
	if err := ensureBootstrap(ctx, ui, runner, cfg, resp.GetBootstrap(), environmentv1.Tier_TIER_PREVIEW, out, in); err != nil {
		return preflightFacts{}, err
	}
	site, err := requirePreviewDomain(cfg, resp.GetPreviewWildcard(), resp.GetIdentity(), pointer, ui)
	if err != nil {
		return preflightFacts{}, err
	}
	return preflightFacts{
		knownSlugs:     resp.GetKnownSlugs(),
		compute:        compute,
		containerArchs: resp.GetContainerArchs(),
		urls: appurl.Preview(cfg, func(app string) string {
			return site.Host(pointer, app)
		}),
	}, nil
}

func preflightDeploy(ctx context.Context, deps cmddeps.Deps, ui *runui.Session, runner *providerclient.Runner, cfg *projectconfig.Config, out io.Writer, in io.Reader) (preflightFacts, error) {
	domains := preflight.Names(preflight.Hostnames(cfg, "production"))
	resp, err := preflight.Run(ctx, ui, runner, cfg, environmentv1.Tier_TIER_PRODUCTION, slugToScopeBy(ui, domains, cfg), domains, preflight.Frameworks(cfg), "ocel bootstrap production")
	if err != nil {
		return preflightFacts{}, err
	}
	compute, err := preflight.ResolveComputes(cfg, resp.GetComputes(), runner.Name())
	if err != nil {
		return preflightFacts{}, err
	}
	if err := appregistry.RequireSecret(cfg); err != nil {
		return preflightFacts{}, err
	}
	if err := deps.RequireImageBuilder(ctx, ui, cfg, resp.GetContainerArchs()); err != nil {
		return preflightFacts{}, err
	}
	if err := refuseClaimedDomains(resp.GetDomainClaims(), filepath.Base(cfg.Path), ui.Warning); err != nil {
		return preflightFacts{}, err
	}
	if err := ensureBootstrap(ctx, ui, runner, cfg, resp.GetBootstrap(), environmentv1.Tier_TIER_PRODUCTION, out, in); err != nil {
		return preflightFacts{}, err
	}
	return preflightFacts{knownSlugs: resp.GetKnownSlugs(), compute: compute, containerArchs: resp.GetContainerArchs(), urls: appurl.Production(cfg)}, nil
}

func ensureBootstrap(ctx context.Context, ui *runui.Session, runner *providerclient.Runner, cfg *projectconfig.Config, status *contractv1.BootstrapStatus, tier environmentv1.Tier, out io.Writer, in io.Reader) error {
	if ui.Dry() {
		return bootstrap.PlanFor(status).Insist(tier)
	}
	return bootstrap.Offer(ctx, runner, status, tier, edgewire.Selection(cfg), ui, ui.Interactive(), out, in)
}

func slugToScopeBy(ui *runui.Session, domains []string, cfg *projectconfig.Config) string {
	if ui.Interactive() || len(domains) > 0 {
		return cfg.Slug
	}
	return ""
}

func guardNewProject(ctx context.Context, ui *runui.Session, cfg *projectconfig.Config, knownSlugs []string) (bool, error) {
	if len(knownSlugs) == 0 {
		return true, nil
	}
	ui.Warning(fmt.Sprintf("No existing deployment for slug %q.\nThis will create a NEW project.\nThis backend already has: %s",
		cfg.Slug, strings.Join(knownSlugs, ", ")))
	return ui.Guard(ctx, "Continue?")
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
