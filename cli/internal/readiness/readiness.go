package readiness

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/ocelhq/ocel/cli/internal/clierror"
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
	RequireHostname  bool
	Feature          string
}

func Check(ctx context.Context, span *run.Span, provider *providerprocess.Provider, cfg *project.Project, req Request) (Preflight, error) {
	child := span.Child(provider.Name(), CheckingTitle(req.Tier, cfg.Slug))
	read, err := Read(ctx, provider, cfg, req)
	if err == nil {
		span.Identity(identityEvent(cfg, req.Tier, read.Response.GetIdentity()))
		err = RefuseUnready(read.Response, provider, cfg, req)
	}
	if err == nil && req.Feature != "" {
		err = refuseMissingOfferedFeature(ctx, provider, cfg, read.Response.GetBootstrap(), req)
	}
	child.End(err)
	if err != nil {
		return Preflight{}, err
	}
	return read, nil
}

func RefuseUnready(resp *contractv1.PreflightResponse, provider *providerprocess.Provider, cfg *project.Project, req Request) error {
	if req.Require == None {
		return nil
	}
	if err := refuseCredentialProblems(resp.GetCredentialProblems()); err != nil {
		return err
	}
	if req.Require == Credentials {
		return nil
	}
	if err := refuseAbsentInfrastructure(resp, provider, cfg, req.Tier); err != nil {
		return err
	}
	if req.Require == Features {
		if err := refuseMissingFeatures(NewGap(resp.GetBootstrap()), provider, cfg, req.Tier); err != nil {
			return err
		}
	}
	if req.RequireHostname && resp.GetHostnameRequired() && len(cfg.HostnameNames(req.Tier)) == 0 {
		return NoHostnameError{Slug: cfg.Slug, ConfigPath: cfg.Path, Vendor: resp.GetIdentity().GetProvider()}
	}
	return nil
}

func refuseAbsentInfrastructure(resp *contractv1.PreflightResponse, provider *providerprocess.Provider, cfg *project.Project, tier environmentv1.Tier) error {
	infra := resp.GetInfraTier()
	if resp.GetInfrastructurePresent() && infra == tier {
		return nil
	}
	if resp.GetInfrastructurePresent() && tier == environmentv1.Tier_TIER_UNSPECIFIED {
		return fmt.Errorf("the account points at %s, but this command requires %s", infraLabel(infra), infraLabel(tier))
	}
	absent := NoInfrastructureError{
		Tier:     tier,
		Features: requiredFeatures(resp.GetBootstrap()),
		Account:  resp.GetIdentity().GetAccount(),
		Edge:     cfg.EdgeSelection(),
		Provider: provider,
	}
	if resp.GetInfrastructurePresent() {
		absent.PresentTier = infra
	}
	return &clierror.Error{Code: clierror.CodeBootstrapMissing, Hint: absent.Hint(), Cause: absent}
}

func refuseMissingFeatures(gap Gap, provider *providerprocess.Provider, cfg *project.Project, tier environmentv1.Tier) error {
	if len(gap.Missing) == 0 {
		return nil
	}
	return MissingFeaturesError{Gap: gap, Tier: tier, Edge: cfg.EdgeSelection(), Provider: provider}.refuse()
}

func refuseMissingOfferedFeature(ctx context.Context, provider *providerprocess.Provider, cfg *project.Project, status *contractv1.BootstrapStatus, req Request) error {
	offered, err := hasOffer(ctx, provider, req.Tier, cfg.EdgeSelection(), req.Feature)
	if err != nil || !offered {
		return err
	}
	return refuseMissingFeatures(NewFeatureGap(status, req.Feature), provider, cfg, req.Tier)
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

func CheckingTitle(tier environmentv1.Tier, slug string) progress.Title {
	if tier == environmentv1.Tier_TIER_UNSPECIFIED {
		return progress.Checking.Title("your credentials")
	}
	object := "your credentials and the " + TierName(tier) + " bootstrap"
	if slug != "" {
		object += " for " + slug
	}
	return progress.Checking.Title(object)
}

func infraLabel(tier environmentv1.Tier) string {
	if tier == environmentv1.Tier_TIER_UNSPECIFIED {
		return "no Ocel infrastructure"
	}
	return TierName(tier) + " infrastructure"
}

func identityEvent(cfg *project.Project, tier environmentv1.Tier, id *contractv1.Identity) *streamv1.IdentityEvent {
	event := &streamv1.IdentityEvent{Project: cfg.Slug, Tier: tier}
	if id.GetProvider() != "" || id.GetAccount() != "" || id.GetPrincipal() != "" || id.GetLocation() != "" {
		event.Origin = &streamv1.Party{
			Vendor:    id.GetProvider(),
			Account:   id.GetAccount(),
			Principal: id.GetPrincipal(),
			Location:  id.GetLocation(),
		}
	}
	if scope := id.GetEdgeScope(); scope != "" {
		event.Edge = &streamv1.Party{Vendor: edgeVendor(cfg), Account: scope}
	}
	return event
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
