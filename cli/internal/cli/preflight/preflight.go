package preflight

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/cli/internal/cli/bootstrap"
	"github.com/ocelhq/ocel/cli/internal/edgewire"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/internal/provider"
	"github.com/ocelhq/ocel/cli/internal/runui"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

func Run(ctx context.Context, rep runui.Reporter, runner *provider.Runner, cfg *projectconfig.Config, required environmentv1.Tier, slug string, domains []string, frameworks []string, bootstrapHint string) (*contractv1.PreflightResponse, error) {
	client, err := runner.Client()
	if err != nil {
		return nil, err
	}

	spinner := rep.Spin("Checking credentials")
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
	if rep.Presentation().TTY {
		rep.Diagnostic(identityBanner(resp.GetIdentity()))
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

func Tier(ctx context.Context, rep runui.Reporter, runner *provider.Runner, cfg *projectconfig.Config, required environmentv1.Tier, bootstrapHint string) error {
	resp, err := Run(ctx, rep, runner, cfg, required, "", nil, Frameworks(cfg), bootstrapHint)
	if err != nil {
		return err
	}
	return bootstrap.PlanFor(resp.GetBootstrap()).Refusal(required)
}

func Credentials(ctx context.Context, rep runui.Reporter, runner *provider.Runner, cfg *projectconfig.Config, required environmentv1.Tier, bootstrapHint string) error {
	_, err := Run(ctx, rep, runner, cfg, required, "", nil, nil, bootstrapHint)
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

type Hostname struct {
	Name string
	App  string
}

func Hostnames(cfg *projectconfig.Config, class string) []Hostname {
	var hosts []Hostname
	seen := map[string]bool{}
	add := func(domains map[string][]string, app string) {
		for _, host := range domains[class] {
			if seen[host] {
				continue
			}
			seen[host] = true
			hosts = append(hosts, Hostname{Name: host, App: app})
		}
	}
	add(cfg.Domains, "")
	for _, app := range cfg.Apps {
		add(app.Domains, app.Name)
	}
	return hosts
}

func Names(hosts []Hostname) []string {
	named := make([]string, 0, len(hosts))
	for _, host := range hosts {
		named = append(named, host.Name)
	}
	return named
}

func Configured(hosts []Hostname) []*contractv1.ConfiguredHostname {
	wired := make([]*contractv1.ConfiguredHostname, 0, len(hosts))
	for _, host := range hosts {
		wired = append(wired, &contractv1.ConfiguredHostname{Hostname: host.Name, App: host.App})
	}
	return wired
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
