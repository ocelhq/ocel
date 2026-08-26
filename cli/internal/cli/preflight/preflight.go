package preflight

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/cli/internal/cli/bootstrap"
	"github.com/ocelhq/ocel/cli/internal/cli/cmddeps"
	"github.com/ocelhq/ocel/cli/internal/deployui"
	"github.com/ocelhq/ocel/cli/internal/edgewire"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/internal/provider"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

func Run(ctx context.Context, deps cmddeps.Deps, runner *provider.Runner, cfg *projectconfig.Config, required environmentv1.Tier, slug string, domains []string, frameworks []string, bootstrapHint string, out io.Writer) (*contractv1.PreflightResponse, error) {
	client, err := runner.Client()
	if err != nil {
		return nil, err
	}

	spinner := deployui.StartSpinner(out, "Checking credentials")
	resp, err := client.Preflight(ctx, &contractv1.PreflightRequest{
		RequiredTier: required,
		Slug:         slug,
		Domains:      domains,
		Frameworks:   frameworks,
		Edge:         edgewire.Selection(cfg),
	})
	spinner.Stop()
	if err != nil {
		return nil, err
	}
	if deps.StdoutIsTerminal(out) {
		fmt.Fprint(out, identityBanner(resp.GetIdentity()))
	}
	if err := credentialProblems(resp.GetCredentialProblems()); err != nil {
		return nil, err
	}
	if !resp.GetInfrastructurePresent() {
		return nil, fmt.Errorf("no infrastructure is set up yet; run `%s` to create it", bootstrapHint)
	}
	if err := checkTier(resp.GetInfraTier(), required); err != nil {
		return nil, err
	}
	return resp, nil
}

func Tier(ctx context.Context, deps cmddeps.Deps, runner *provider.Runner, cfg *projectconfig.Config, required environmentv1.Tier, bootstrapHint string, out io.Writer) error {
	resp, err := Run(ctx, deps, runner, cfg, required, "", nil, Frameworks(cfg), bootstrapHint, out)
	if err != nil {
		return err
	}
	return bootstrap.PlanFor(resp.GetBootstrap()).Refusal(required)
}

func Credentials(ctx context.Context, deps cmddeps.Deps, runner *provider.Runner, cfg *projectconfig.Config, required environmentv1.Tier, bootstrapHint string, out io.Writer) error {
	_, err := Run(ctx, deps, runner, cfg, required, "", nil, nil, bootstrapHint, out)
	return err
}

func Frameworks(cfg *projectconfig.Config) []string {
	var frameworks []string
	for _, app := range cfg.Apps {
		if app.Framework != "" {
			frameworks = append(frameworks, app.Framework)
		}
	}
	slices.Sort(frameworks)
	return slices.Compact(frameworks)
}

func Hostnames(cfg *projectconfig.Config, class string) []string {
	var hosts []string
	seen := map[string]bool{}
	add := func(domains map[string][]string) {
		for _, host := range domains[class] {
			if seen[host] {
				continue
			}
			seen[host] = true
			hosts = append(hosts, host)
		}
	}
	add(cfg.Domains)
	for _, app := range cfg.Apps {
		add(app.Domains)
	}
	return hosts
}

func checkTier(infra, required environmentv1.Tier) error {
	if infra == required {
		return nil
	}

	switch required {
	case environmentv1.Tier_TIER_PREVIEW:
		return fmt.Errorf(
			"ocel preview can only run against preview infrastructure, but the account points at %s; run `ocel bootstrap preview` to set it up",
			infraLabel(infra),
		)
	case environmentv1.Tier_TIER_PRODUCTION:
		return fmt.Errorf(
			"ocel deploy can only run against production infrastructure, but the account points at %s; run `ocel bootstrap production` to set it up",
			infraLabel(infra),
		)
	default:
		return fmt.Errorf(
			"the account points at %s, but this command requires %s",
			infraLabel(infra), infraLabel(required),
		)
	}
}

func infraLabel(tier environmentv1.Tier) string {
	switch tier {
	case environmentv1.Tier_TIER_PREVIEW:
		return "preview infrastructure"
	case environmentv1.Tier_TIER_PRODUCTION:
		return "production infrastructure"
	default:
		return "no Ocel infrastructure"
	}
}

func identityBanner(id *contractv1.Identity) string {
	if id == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("Running with:\n")
	wrote := false
	if line := originIdentityLine(id); line != "" {
		fmt.Fprintf(&b, "  %-11s %s\n", id.GetProvider(), line)
		wrote = true
	}
	if scope := id.GetEdgeScope(); scope != "" {
		fmt.Fprintf(&b, "  %-11s account=%s\n", "Edge", scope)
		wrote = true
	}
	if !wrote {
		return ""
	}
	return b.String()
}

func originIdentityLine(id *contractv1.Identity) string {
	if id.GetAccount() == "" {
		return ""
	}
	parts := []string{"account=" + id.GetAccount()}
	if principal := id.GetPrincipal(); principal != "" {
		parts = append(parts, "identity="+principal)
	}
	for _, detail := range id.GetDetails() {
		parts = append(parts, detail.GetLabel()+"="+detail.GetValue())
	}
	return strings.Join(parts, "  ")
}

func credentialProblems(problems []*contractv1.CredentialProblem) error {
	if len(problems) == 0 {
		return nil
	}
	var b strings.Builder
	b.WriteString("credential check failed:")
	for _, p := range problems {
		fmt.Fprintf(&b, "\n  ✗ %s: %s", p.GetProvider(), p.GetMessage())
		if h := p.GetHint(); h != "" {
			fmt.Fprintf(&b, "\n    → %s", h)
		}
	}
	return errors.New(b.String())
}
