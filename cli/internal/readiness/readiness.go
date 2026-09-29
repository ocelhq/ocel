package readiness

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/providerprocess"
	"github.com/ocelhq/ocel/cli/internal/run"
	"github.com/ocelhq/ocel/pkg/progress"
	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

type Requirement int

const (
	None Requirement = iota
	Credentials
	Infrastructure
	Features
)

type Request struct {
	Tier             environmentv1.Tier
	Require          Requirement
	Slug             string
	Domains          []string
	CheckHosts       bool
	HostCheckDomains []string
}

func Check(ctx context.Context, span *run.Span, provider *providerprocess.Provider, cfg *project.Project, req Request) (*contractv1.PreflightResponse, error) {
	unit := span.Unit(provider.Name(), checkingTitle(req.Tier, cfg.Slug))
	resp, err := Read(ctx, provider, cfg, req)
	if err == nil {
		span.Identity(identityEvent(cfg, req.Tier, resp.GetIdentity()))
		err = RefuseUnready(resp, req.Tier, req.Require)
	}
	unit.End(err)
	if err != nil {
		return nil, err
	}
	return resp, nil
}

func RefuseUnready(resp *contractv1.PreflightResponse, tier environmentv1.Tier, require Requirement) error {
	if require == None {
		return nil
	}
	if err := refuseCredentialProblems(resp.GetCredentialProblems()); err != nil {
		return err
	}
	if require == Credentials {
		return nil
	}
	if !resp.GetInfrastructurePresent() {
		return fmt.Errorf("no infrastructure is set up yet; run `%s` to create it", BootstrapCommand(tier))
	}
	if err := refuseOtherTier(resp.GetInfraTier(), tier); err != nil {
		return err
	}
	if require == Infrastructure {
		return nil
	}
	return NewGap(resp.GetBootstrap()).RefuseMissing(tier)
}

func TierName(tier environmentv1.Tier) string {
	if tier == environmentv1.Tier_TIER_PREVIEW {
		return "preview"
	}
	return "production"
}

func BootstrapCommand(tier environmentv1.Tier) string {
	return "ocel bootstrap " + TierName(tier)
}

func checkingTitle(tier environmentv1.Tier, slug string) progress.Title {
	if tier == environmentv1.Tier_TIER_UNSPECIFIED {
		return progress.Checking.Title("your credentials")
	}
	object := "your credentials and the " + TierName(tier) + " bootstrap"
	if slug != "" {
		object += " for " + slug
	}
	return progress.Checking.Title(object)
}

func refuseOtherTier(infra, required environmentv1.Tier) error {
	if infra == required {
		return nil
	}
	if required == environmentv1.Tier_TIER_UNSPECIFIED {
		return fmt.Errorf("the account points at %s, but this command requires %s", infraLabel(infra), infraLabel(required))
	}
	return fmt.Errorf(
		"this command needs %s infrastructure, but the account points at %s; run `%s` to set it up",
		TierName(required), infraLabel(infra), BootstrapCommand(required),
	)
}

func infraLabel(tier environmentv1.Tier) string {
	if tier == environmentv1.Tier_TIER_UNSPECIFIED {
		return "no Ocel infrastructure"
	}
	return TierName(tier) + " infrastructure"
}

func identityEvent(cfg *project.Project, tier environmentv1.Tier, id *contractv1.Identity) *streamv1.IdentityEvent {
	ev := &streamv1.IdentityEvent{Project: cfg.Slug, Tier: tier}
	if id.GetProvider() != "" || id.GetAccount() != "" || id.GetPrincipal() != "" || id.GetLocation() != "" {
		ev.Origin = &streamv1.Party{
			Vendor:    id.GetProvider(),
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

func edgeVendor(cfg *project.Project) string {
	if id := string(cfg.EdgeKind()); id != "" {
		return id
	}
	return "edge"
}

func refuseCredentialProblems(problems []*contractv1.CredentialProblem) error {
	if len(problems) == 0 {
		return nil
	}
	var b strings.Builder
	b.WriteString("credential check failed:")
	for _, problem := range problems {
		fmt.Fprintf(&b, "\n  ✗ %s: %s", problem.GetProvider(), problem.GetMessage())
		if hint := problem.GetHint(); hint != "" {
			fmt.Fprintf(&b, "\n    → %s", hint)
		}
	}
	return errors.New(b.String())
}
