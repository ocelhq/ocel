package vps

import (
	"context"
	"slices"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/bootstrapplan"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
	"github.com/ocelhq/ocel/platform/vps/provider/machine"
)

const consoleTrustSummary = "the console's credentials are exchanged on this box for short-lived ssh certificates"

type consoleTrust interface {
	Read(ctx context.Context, tier environment.Tier) (host.TrustReading, error)
	Apply(ctx context.Context, tier environment.Tier, trust provider.WebIdentityTrust, read host.TrustReading, progress progress.Log) error
	Remove(ctx context.Context, tier environment.Tier, progress progress.Log) error
}

type consoleTrustPart struct {
	provider.Bootstrap
	trust    consoleTrust
	hostname func(ctx context.Context) (string, error)
}

func (p *Provider) addConsoleTrust(box provider.Bootstrap) consoleTrustPart {
	return consoleTrustPart{Bootstrap: box, trust: p.host.ConsoleTrust(), hostname: p.hostname}
}

func (p *Provider) hostname(ctx context.Context) (string, error) {
	dest, err := p.ssh.Destination(ctx)
	return dest.Written, err
}

func isTrustPart(part string) bool { return part == provider.ConsoleTrustPart }

func withoutTrustPart(req provider.BootstrapRequest) provider.BootstrapRequest {
	req.Parts = slices.DeleteFunc(slices.Clone(req.Parts), isTrustPart)
	req.Remove = slices.DeleteFunc(slices.Clone(req.Remove), isTrustPart)
	req.Trust = nil
	return req
}

func (t consoleTrustPart) Parts() []provider.Part {
	return append(slices.Clone(t.Bootstrap.Parts()), provider.Part{Name: provider.ConsoleTrustPart, Summary: consoleTrustSummary})
}

func (t consoleTrustPart) identityProvider(ctx context.Context) (string, error) {
	hostname, err := t.hostname(ctx)
	if err != nil {
		return "", err
	}
	if err := dialable(hostname); err != nil {
		return "", err
	}
	return "https://" + hostname + boxapi.ConnectorPath, nil
}

func (t consoleTrustPart) Describe(ctx context.Context, tier environment.Tier) (provider.BootstrapDescription, error) {
	described, err := t.Bootstrap.Describe(ctx, tier)
	if err != nil || !described.Present {
		return described, err
	}
	read, err := t.trust.Read(ctx, tier)
	if err != nil || read.Trust == nil {
		return described, err
	}
	identity, err := t.identityProvider(ctx)
	if err != nil {
		return provider.BootstrapDescription{}, err
	}
	described.Stacks = append(slices.Clone(described.Stacks), provider.BootstrapStack{
		Name:          provider.ConsoleTrustPart,
		Part:          provider.ConsoleTrustPart,
		Present:       true,
		DigestCurrent: read.IsCurrent(host.ConsoleTrustItems(tier, *read.Trust)),
	})
	described.Identity = &provider.TierIdentity{IdentityProvider: identity}
	described.Trust = read.Trust
	return described, nil
}

func (t consoleTrustPart) DescribeParts(ctx context.Context, described provider.BootstrapDescription) ([]provider.BootstrapPart, error) {
	parts, err := t.Bootstrap.DescribeParts(ctx, described)
	if err != nil || described.Trust == nil {
		return parts, err
	}
	trusted := provider.BootstrapPart{Name: provider.ConsoleTrustPart, Summary: consoleTrustSummary}
	for _, stack := range described.Stacks {
		if stack.Part == provider.ConsoleTrustPart {
			trusted.Current = stack.DigestCurrent
		}
	}
	for _, item := range host.ConsoleTrustItems(described.Tier, *described.Trust) {
		trusted.Resources = append(trusted.Resources, provider.PhysicalResource{Kind: item.Kind, ID: item.Name})
	}
	return append(parts, trusted), nil
}

func (t consoleTrustPart) wanted(ctx context.Context, req provider.BootstrapRequest, read host.TrustReading) (*provider.WebIdentityTrust, error) {
	if !slices.Contains(req.Parts, provider.ConsoleTrustPart) && req.Trust == nil {
		return nil, nil
	}
	if _, err := t.identityProvider(ctx); err != nil {
		return nil, err
	}
	trust := req.Trust
	if trust == nil {
		trust = read.Trust
	}
	if trust == nil {
		return nil, refusal.Refuse(refusal.CodeInvalid,
			"the %s %s part records no console to trust: log in to the console and run `%s` again", req.Tier, provider.ConsoleTrustPart, provider.BootstrapCommand(req.Tier))
	}
	if err := host.RefuseConflictingTrust(req.Tier, *trust, read); err != nil {
		return nil, err
	}
	return trust, nil
}

func (t consoleTrustPart) refuseSharedLogin(ctx context.Context, req provider.BootstrapRequest) error {
	switch req.Tier {
	case environment.TierProduction:
		preview, err := t.trust.Read(ctx, environment.TierPreview)
		if err != nil || preview.Trust == nil {
			return err
		}
		return refusal.Refuse(refusal.CodeInvalid,
			"preview on this box takes the console's credentials, and one %s login serves both tiers, so a preview credential would reach production\nRun `%s --remove %s` first, or give production a box of its own",
			boxapi.DeployLogin, provider.BootstrapCommand(environment.TierPreview), provider.ConsoleTrustPart)
	case environment.TierPreview:
		if slices.Contains(req.Remove, provider.ConsoleTrustPart) || (!slices.Contains(req.Parts, provider.ConsoleTrustPart) && req.Trust == nil) {
			return nil
		}
		production, err := t.Bootstrap.Describe(ctx, environment.TierProduction)
		if err != nil || !production.Present {
			return err
		}
		return refusal.Refuse(refusal.CodeInvalid,
			"this box serves production, and one %s login serves both tiers, so a preview credential from the console would reach production\nDeploy previews from the console or git to a box of their own; a preview you deploy from your machine still works here over your own ssh",
			boxapi.DeployLogin)
	}
	return nil
}

func (t consoleTrustPart) Plan(ctx context.Context, req provider.BootstrapRequest) (provider.Plan, error) {
	if err := t.refuseSharedLogin(ctx, req); err != nil {
		return provider.Plan{}, err
	}
	plan, err := t.Bootstrap.Plan(ctx, withoutTrustPart(req))
	if err != nil {
		return plan, err
	}
	removing := slices.Contains(req.Remove, provider.ConsoleTrustPart)
	if !removing && !slices.Contains(req.Parts, provider.ConsoleTrustPart) && req.Trust == nil {
		return plan, nil
	}
	read, err := t.trust.Read(ctx, req.Tier)
	if err != nil {
		return provider.Plan{}, err
	}
	group := provider.ChangeGroup{Kind: provider.StackGroupKind, Name: provider.ConsoleTrustPart, Part: provider.ConsoleTrustPart}
	if removing {
		if read.Trust == nil {
			return plan, nil
		}
		group.Action = provider.ActionDelete
		group.Changes = []provider.Change{{Kind: machine.KindFile, Name: boxapi.ConsoleTrustPath(req.Tier), Action: provider.ActionDelete}}
	} else {
		trust, err := t.wanted(ctx, req, read)
		if err != nil {
			return provider.Plan{}, err
		}
		group.Changes = read.ListChanges(host.ConsoleTrustItems(req.Tier, *trust))
		group.Action, group.Reason = provider.RollUp(group.Changes)
	}
	plan.Groups = append(plan.Groups, bootstrapplan.PrefixWithVendor(Vendor, []provider.ChangeGroup{group})...)
	return plan, nil
}

func (t consoleTrustPart) Apply(ctx context.Context, req provider.BootstrapRequest, progress progress.Log) error {
	if err := t.refuseSharedLogin(ctx, req); err != nil {
		return err
	}
	if err := t.Bootstrap.Apply(ctx, withoutTrustPart(req), progress); err != nil {
		return err
	}
	if slices.Contains(req.Remove, provider.ConsoleTrustPart) {
		return t.trust.Remove(ctx, req.Tier, progress)
	}
	if !slices.Contains(req.Parts, provider.ConsoleTrustPart) && req.Trust == nil {
		return nil
	}
	read, err := t.trust.Read(ctx, req.Tier)
	if err != nil {
		return err
	}
	trust, err := t.wanted(ctx, req, read)
	if err != nil {
		return err
	}
	return t.trust.Apply(ctx, req.Tier, *trust, read, progress)
}

func (t consoleTrustPart) PlanRemove(ctx context.Context, tier environment.Tier) (provider.Plan, error) {
	plan, err := t.Bootstrap.PlanRemove(ctx, tier)
	if err != nil {
		return plan, err
	}
	read, err := t.trust.Read(ctx, tier)
	if err != nil || read.Trust == nil {
		return plan, err
	}
	plan.Groups = append(plan.Groups, bootstrapplan.PrefixWithVendor(Vendor, []provider.ChangeGroup{{
		Kind: provider.StackGroupKind, Name: provider.ConsoleTrustPart, Part: provider.ConsoleTrustPart, Action: provider.ActionDelete,
		Changes: []provider.Change{{Kind: machine.KindFile, Name: boxapi.ConsoleTrustPath(tier), Action: provider.ActionDelete}},
	}})...)
	return plan, nil
}

func (t consoleTrustPart) Remove(ctx context.Context, tier environment.Tier, progress progress.Log) error {
	read, err := t.trust.Read(ctx, tier)
	if err != nil {
		return err
	}
	if read.Trust != nil {
		if err := t.trust.Remove(ctx, tier, progress); err != nil {
			return err
		}
	}
	return t.Bootstrap.Remove(ctx, tier, progress)
}
