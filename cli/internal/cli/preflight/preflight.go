package preflight

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/cli/internal/edgewire"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/internal/provider"
	"github.com/ocelhq/ocel/cli/internal/runui"
	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

func Run(ctx context.Context, rep runui.Reporter, runner *provider.Runner, cfg *projectconfig.Config, required environmentv1.Tier, slug string, domains []string, runtimes []string, bootstrapHint string) (*contractv1.PreflightResponse, error) {
	resp, err := announce(ctx, rep, runner, cfg, required, slug, domains, runtimes)
	if err != nil {
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

func Announce(ctx context.Context, rep runui.Reporter, runner *provider.Runner, cfg *projectconfig.Config, required environmentv1.Tier) error {
	_, err := announce(ctx, rep, runner, cfg, required, cfg.Slug, nil, Runtimes(cfg))
	return err
}

func announce(ctx context.Context, rep runui.Reporter, runner *provider.Runner, cfg *projectconfig.Config, required environmentv1.Tier, slug string, domains []string, runtimes []string) (*contractv1.PreflightResponse, error) {
	client, err := runner.Client()
	if err != nil {
		return nil, err
	}

	spinner := rep.Spin("Checking credentials")
	resp, err := client.Preflight(ctx, &contractv1.PreflightRequest{
		RequiredTier: required,
		Slug:         slug,
		Domains:      domains,
		Runtimes:     runtimes,
		Edge:         edgewire.Selection(cfg),
	})
	spinner.Stop()
	if err != nil {
		return nil, err
	}
	rep.Identity(IdentityEvent(cfg, required, resp.GetIdentity()))
	if err := credentialProblems(resp.GetCredentialProblems()); err != nil {
		return nil, err
	}
	return resp, nil
}

func Credentials(ctx context.Context, rep runui.Reporter, runner *provider.Runner, cfg *projectconfig.Config, required environmentv1.Tier, bootstrapHint string) error {
	_, err := Run(ctx, rep, runner, cfg, required, "", nil, nil, bootstrapHint)
	return err
}

func Runtimes(cfg *projectconfig.Config) []string {
	var runtimes []string
	for _, app := range cfg.Apps {
		if app.Runtime.Name != "" {
			runtimes = append(runtimes, app.Runtime.Name)
		}
	}
	slices.Sort(runtimes)
	return slices.Compact(runtimes)
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
			"this command needs preview infrastructure, but the account points at %s; run `ocel bootstrap preview` to set it up",
			infraLabel(infra),
		)
	case environmentv1.Tier_TIER_PRODUCTION:
		return fmt.Errorf(
			"this command needs production infrastructure, but the account points at %s; run `ocel bootstrap production` to set it up",
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

func IdentityEvent(cfg *projectconfig.Config, tier environmentv1.Tier, id *contractv1.Identity) *streamv1.IdentityEvent {
	ev := &streamv1.IdentityEvent{Project: cfg.Slug, Tier: tier}
	if id.GetProvider() != "" || id.GetAccount() != "" || id.GetPrincipal() != "" || id.GetLocation() != "" {
		ev.Origin = &streamv1.Party{
			Vendor:    strings.ToLower(id.GetProvider()),
			Account:   id.GetAccount(),
			Principal: id.GetPrincipal(),
			Location:  id.GetLocation(),
		}
	}
	if scope := id.GetEdgeScope(); scope != "" {
		ev.Edge = &streamv1.Party{Vendor: edgeVendor(cfg), Account: scope}
	}
	return ev
}

func edgeVendor(cfg *projectconfig.Config) string {
	if kind := string(cfg.EdgeKind()); kind != "" {
		return kind
	}
	return "edge"
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
