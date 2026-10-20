package release

import (
	"context"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/gcp/provider/assets"
	"github.com/ocelhq/ocel/platform/gcp/provider/component"
	"github.com/ocelhq/ocel/platform/gcp/provider/compute/next"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/cloudrun"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/grants"
	"github.com/ocelhq/ocel/platform/gcp/provider/resources/variables"
)

const (
	appDescriptionPrefix      = "the identity app "
	appDescriptionProject     = " of project "
	appDescriptionEnvironment = " runs as in environment "
	appDescriptionTier        = " of the "
	appDescriptionSuffix      = " tier"
)

func (p *Release) EnsureAccount(ctx context.Context, c *gcloud.Clients, spec provider.StackSpec, declared map[string]*provider.TopicSpec) (string, error) {
	ref, app := spec.Ref, spec.App.App
	account := c.AppAccount(ref.Project, ref.Name.Env, app)
	if err := CreateAppAccount(ctx, c, ref, app); err != nil {
		return "", err
	}
	email := c.AppAccountEmail(ref.Project, ref.Name.Env, app)
	referenced, err := variables.ReferencedOwners(ctx, p.Records, p.Cipher, spec)
	if err != nil {
		return "", err
	}
	want, err := AppGrants(ctx, c, p.Components, p.CacheGrants, spec, account, "serviceAccount:"+email, declared, referenced)
	if err != nil {
		return "", err
	}
	if err := grants.Reconcile(ctx, c, p.Records, ref.Tier, account, want); err != nil {
		return "", err
	}
	return email, nil
}

func AppGrants(ctx context.Context, c *gcloud.Clients, registry component.Registry, cacheGrants CacheGrants, spec provider.StackSpec, account, member string, declared map[string]*provider.TopicSpec, referenced []string) ([]grants.Grant, error) {
	var granted []grants.Grant
	for _, project := range append([]string{spec.Ref.Project}, referenced...) {
		granted = append(granted,
			grants.OnProject(grants.RecordsRole, member, variables.ProjectDatabaseCondition(c, spec.Ref.Tier, project)),
			grants.OnProject(grants.OpeningRole, member, variables.ProjectKeyCondition(c, spec.Ref.Tier, project)))
	}
	held, err := registry.Grants(ctx, component.App{Clients: c, Spec: spec, Account: account, Member: member, Topics: declared})
	if err != nil {
		return nil, err
	}
	granted = append(granted, held...)
	cache, err := cacheGrants(ctx, c, spec, member)
	if err != nil {
		return nil, err
	}
	granted = append(granted, cache...)
	assets, err := assets.Grants(ctx, c, spec, member)
	if err != nil {
		return nil, err
	}
	granted = append(granted, assets...)
	granted = append(granted, next.CDNPurgeGrants(c, spec, member)...)
	gated, err := cloudrun.GateGrants(c, spec, member)
	if err != nil {
		return nil, err
	}
	return append(granted, gated...), nil
}

func CreateAppAccount(ctx context.Context, c *gcloud.Clients, ref provider.StackRef, app string) error {
	return c.CreateAccount(ctx, c.AppAccount(ref.Project, ref.Name.Env, app),
		gcloud.Clipped("ocel "+ref.Project+"/"+app+"@"+ref.Name.Env+" ("+string(ref.Tier)+")", gcloud.MaxDisplayNameBytes),
		AppAccountDescription(ref.Tier, ref.Project, ref.Name.Env, app))
}

func AppAccountDescription(tier environment.Tier, project, env, app string) string {
	return appDescriptionPrefix + app + appDescriptionProject + project + appDescriptionEnvironment + env + appDescriptionTier + string(tier) + appDescriptionSuffix
}
