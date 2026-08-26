package deploy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/ocelhq/ocel/cli/internal/cli/bootstrap"
	"github.com/ocelhq/ocel/cli/internal/cli/cmddeps"
	"github.com/ocelhq/ocel/cli/internal/cli/preflight"
	"github.com/ocelhq/ocel/cli/internal/edgewire"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/internal/provider"
	"github.com/ocelhq/ocel/cli/internal/runui"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func preflightPreview(ctx context.Context, present runui.Presentation, runner *provider.Runner, cfg *projectconfig.Config, out io.Writer) error {
	return preflight.Tier(ctx, present, runner, cfg, environmentv1.Tier_TIER_PREVIEW, "ocel bootstrap preview", out)
}

func preflightPreviewUp(ctx context.Context, deps cmddeps.Deps, present runui.Presentation, runner *provider.Runner, cfg *projectconfig.Config, pointer string, out io.Writer, stdin io.Reader) error {
	resp, err := preflight.Run(ctx, present, runner, cfg, environmentv1.Tier_TIER_PREVIEW, cfg.Slug, preflight.Hostnames(cfg, "preview"), preflight.Frameworks(cfg), "ocel bootstrap preview", out)
	if err != nil {
		return err
	}
	if err := refuseClaimedDomains(resp.GetDomainClaims(), filepath.Base(cfg.Path)); err != nil {
		return err
	}
	if err := bootstrap.Offer(ctx, runner, resp.GetBootstrap(), environmentv1.Tier_TIER_PREVIEW, edgewire.Selection(cfg), deps.StdinIsTerminal(stdin), out); err != nil {
		return err
	}
	return requirePreviewDomain(cfg, resp.GetPreviewWildcard(), resp.GetIdentity(), pointer, out)
}

func preflightDeploy(ctx context.Context, deps cmddeps.Deps, present runui.Presentation, runner *provider.Runner, cfg *projectconfig.Config, interactive bool, out io.Writer, stdin io.Reader) ([]string, error) {
	domains := preflight.Hostnames(cfg, "production")
	var slug string
	if interactive || len(domains) > 0 {
		slug = cfg.Slug
	}
	resp, err := preflight.Run(ctx, present, runner, cfg, environmentv1.Tier_TIER_PRODUCTION, slug, domains, preflight.Frameworks(cfg), "ocel bootstrap production", out)
	if err != nil {
		return nil, err
	}
	if err := refuseClaimedDomains(resp.GetDomainClaims(), filepath.Base(cfg.Path)); err != nil {
		return nil, err
	}
	if err := bootstrap.Offer(ctx, runner, resp.GetBootstrap(), environmentv1.Tier_TIER_PRODUCTION, edgewire.Selection(cfg), interactive, out); err != nil {
		return nil, err
	}
	return resp.GetKnownSlugs(), nil
}

func refuseClaimedDomains(claims []*contractv1.DomainClaim, configName string) error {
	var b strings.Builder
	for _, claim := range claims {
		if claim.GetStatus() != contractv1.DomainClaim_STATUS_CLAIMED {
			continue
		}
		if claim.GetOwner() == edge.PreviewEntryOwner {
			continue
		}
		if b.Len() == 0 {
			b.WriteString("another project already serves a hostname this project declares:")
		}
		fmt.Fprintf(&b, "\n  ✗ %s is held by %s", claim.GetHostname(), claim.GetOwner())
	}
	if b.Len() == 0 {
		return nil
	}
	b.WriteString("\n    → a hostname belongs to one project, so deploying would take it over: remove it from this project's " +
		configName + ", or tear the owning project down (`ocel destroy production` / `ocel destroy preview` in it), then deploy again")
	return errors.New(b.String())
}
