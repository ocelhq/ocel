package gcp

import (
	"context"
	"slices"
	"time"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/originsecret"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/alb"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/albpart"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/cloudflareorigin"
	"github.com/ocelhq/ocel/platform/gcp/provider/resources/variables"
)

const (
	runAPI     = "run.googleapis.com"
	signingAPI = "iamcredentials.googleapis.com"
)

func (p *Provider) PlanPreflight(ctx context.Context, pre provider.DeployPreflight) (provider.Plan, error) {
	if err := p.options.refuseUnknownApps(pre.Deploy.Apps); err != nil {
		return provider.Plan{}, err
	}
	parts, err := p.partsUsedBy(pre)
	if err != nil {
		return provider.Plan{}, err
	}
	apis := p.listAPIsToCheck(pre)
	keeps := newDeployUse(pre).keepsProjectRecords()
	if !keeps && len(parts) == 0 && len(apis) == 0 {
		return provider.Plan{}, nil
	}
	b, err := bootstrapGate{p}.openBootstrap(ctx)
	if err != nil {
		return provider.Plan{}, err
	}
	if err := b.RefuseServicesOff(ctx, apis); err != nil {
		return provider.Plan{}, err
	}
	var plan provider.Plan
	if keeps {
		if plan.Groups, err = variables.PlanProjectDatabase(ctx, b, pre.Deploy.Tier, pre.Deploy.Slug, p.options.OwnDatabase); err != nil {
			return provider.Plan{}, err
		}
	}
	groups, err := b.PlanParts(ctx, pre.Deploy.Tier, parts)
	if err != nil {
		return provider.Plan{}, err
	}
	plan.Groups = append(plan.Groups, groups...)
	return plan, nil
}

func (p *Provider) ApplyPreflight(ctx context.Context, pre provider.DeployPreflight) error {
	if err := p.options.refuseUnknownApps(pre.Deploy.Apps); err != nil {
		return err
	}
	parts, err := p.partsUsedBy(pre)
	if err != nil {
		return err
	}
	if err := p.refuseAPIsOff(ctx, pre); err != nil {
		return err
	}
	if err := p.warnStaleOriginSecret(ctx, pre); err != nil {
		return err
	}
	if pre.Edge != alb.Kind {
		if err := p.leaveALB(ctx, pre); err != nil {
			return err
		}
	}
	if err := p.addParts(ctx, pre, parts); err != nil {
		return err
	}
	if pre.Deploy.Slug == "" {
		return nil
	}
	if newDeployUse(pre).keepsProjectRecords() {
		if err := p.ensureProjectDatabase(ctx, pre); err != nil {
			return err
		}
	}
	slices.Sort(parts)
	return stackrecords.RecordPartsUsed(ctx, p.KeyValues(), pre.Deploy.Tier, pre.Deploy.Slug, parts)
}

func (p *Provider) warnStaleOriginSecret(ctx context.Context, pre provider.DeployPreflight) error {
	if pre.Progress == nil || pre.Edge == edge.None || !slices.ContainsFunc(pre.Deploy.Apps, servesCompute) {
		return nil
	}
	front, err := p.Edges().Open(pre.Edge, nil)
	if err != nil || !front.Facts().RunsCode {
		return err
	}
	c, err := p.openClients(ctx)
	if err != nil {
		return err
	}
	secret, found, err := cloudflareorigin.ReadOriginSecret(ctx, c, pre.Deploy.Tier, pre.Edge)
	if err != nil || !found {
		return err
	}
	if notice := originsecret.FormatStaleNotice(secret, time.Now(), pre.Deploy.Tier); notice != "" {
		pre.Progress.Warn(notice)
	}
	return nil
}

func servesCompute(app provider.AppEntry) bool {
	return app.Compute() == provider.ComputeContainer || len(app.Manifest.GetServerless().GetFunctions()) > 0
}

func (p *Provider) partsUsedBy(pre provider.DeployPreflight) ([]string, error) {
	return newDeployUse(pre).partNames(p.components())
}

func (p *Provider) listAPIsToCheck(pre provider.DeployPreflight) []string {
	if p.skipChecks {
		return nil
	}
	return deployAPIs(pre)
}

func deployAPIs(pre provider.DeployPreflight) []string {
	var apis []string
	if len(pre.Deploy.Apps) > 0 {
		apis = append(apis, runAPI)
	}
	if slices.ContainsFunc(pre.Resources, func(resource provider.Resource) bool { return resource.Type == provider.BindingBucket }) {
		apis = append(apis, signingAPI)
	}
	return apis
}

func (p *Provider) refuseAPIsOff(ctx context.Context, pre provider.DeployPreflight) error {
	apis := p.listAPIsToCheck(pre)
	if len(apis) == 0 {
		return nil
	}
	b, err := bootstrapGate{p}.openBootstrap(ctx)
	if err != nil {
		return err
	}
	return b.RefuseServicesOff(ctx, apis)
}

func (p *Provider) ensureProjectDatabase(ctx context.Context, pre provider.DeployPreflight) error {
	b, err := bootstrapGate{p}.openBootstrap(ctx)
	if err != nil {
		return err
	}
	return variables.EnsureProjectDatabase(ctx, b, pre.Deploy.Tier, pre.Deploy.Slug, p.options.OwnDatabase, pre.Progress, appDatabaseLayouts()...)
}

func (p *Provider) leaveALB(ctx context.Context, pre provider.DeployPreflight) error {
	if pre.Deploy.Slug == "" {
		return nil
	}
	b, err := bootstrapGate{p}.openBootstrap(ctx)
	if err != nil {
		return err
	}
	return albpart.Release(ctx, b, pre.Deploy.Tier, pre.Deploy.Slug, false, pre.Progress)
}

func (p *Provider) addParts(ctx context.Context, pre provider.DeployPreflight, names []string) error {
	if len(names) == 0 {
		return nil
	}
	b, err := bootstrapGate{p}.openBootstrap(ctx)
	if err != nil {
		return err
	}
	return b.AddParts(ctx, pre, names)
}
