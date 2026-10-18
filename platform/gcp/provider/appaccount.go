package gcp

import (
	"context"
	"maps"
	"slices"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/grants"
	"github.com/ocelhq/ocel/platform/gcp/provider/topics"
)

const (
	appDescriptionPrefix      = "the identity app "
	appDescriptionProject     = " of project "
	appDescriptionEnvironment = " runs as in environment "
	appDescriptionTier        = " of the "
	appDescriptionSuffix      = " tier"
)

func (p *Provider) ensureAppAccount(ctx context.Context, c *gcloud.Clients, spec provider.StackSpec, declared map[string]*provider.TopicSpec) (string, error) {
	ref, app := spec.Ref, spec.App.App
	account := c.AppAccount(ref.Tier, ref.Project, ref.Name.Env, app)
	if err := createAppAccount(ctx, c, ref, app); err != nil {
		return "", err
	}
	email := c.AppAccountEmail(ref.Tier, ref.Project, ref.Name.Env, app)
	referenced, err := p.referencedOwners(ctx, spec)
	if err != nil {
		return "", err
	}
	want, err := appGrants(ctx, c, spec, account, "serviceAccount:"+email, declared, referenced)
	if err != nil {
		return "", err
	}
	if err := grants.Reconcile(ctx, c, p.KeyValues(), ref.Tier, account, want); err != nil {
		return "", err
	}
	return email, nil
}

func appGrants(ctx context.Context, c *gcloud.Clients, spec provider.StackSpec, account, member string, declared map[string]*provider.TopicSpec, referenced []string) ([]grants.Grant, error) {
	var granted []grants.Grant
	for _, project := range append([]string{spec.Ref.Project}, referenced...) {
		granted = append(granted,
			grants.OnProject(grants.RecordsRole, member, projectDatabaseCondition(c, spec.Ref.Tier, project)),
			grants.OnProject(grants.OpeningRole, member, projectKeyCondition(c, spec.Ref.Tier, project)))
	}
	if reachesTopics(spec.App) {
		tasks, err := taskGrants(ctx, c, spec, account, member, declared)
		if err != nil {
			return nil, err
		}
		granted = append(granted, tasks...)
	}
	granted = append(granted, bucketGrants(spec, account, member)...)
	cache, err := cacheGrants(c, spec, member)
	if err != nil {
		return nil, err
	}
	granted = append(granted, cache...)
	assets, err := assetsGrants(c, spec, member)
	if err != nil {
		return nil, err
	}
	granted = append(granted, assets...)
	granted = append(granted, cdnPurgeGrants(c, spec, member)...)
	return append(granted, gateGrants(c, spec, member)...), nil
}

func taskGrants(ctx context.Context, c *gcloud.Clients, spec provider.StackSpec, account, member string, declared map[string]*provider.TopicSpec) ([]grants.Grant, error) {
	agent, err := c.ReadServiceAgent(ctx, cloudTasksAgentDomain)
	if err != nil {
		return nil, err
	}
	granted := []grants.Grant{
		grants.OnProject(grants.TaskRecordsRole, member, taskDatabaseCondition(c, spec.Ref.Tier)),
		grants.On(grants.Queue, c.DelayQueuePath(c.Region, spec.Ref.Tier), queueEnqueuerRole, member),
		grants.On(grants.Account, account, runAsRole, member),
		grants.On(grants.Account, account, runAsRole, agent),
	}
	names := taskNames(c.Names, spec.Ref)
	for _, name := range slices.Sorted(maps.Keys(declared)) {
		granted = append(granted, grants.On(grants.Topic, topics.TopicPath(c.Project(), names.Topic(name)), topics.PublisherRole, member))
	}
	return granted, nil
}

func createAppAccount(ctx context.Context, c *gcloud.Clients, ref provider.StackRef, app string) error {
	return c.CreateAccount(ctx, c.AppAccount(ref.Tier, ref.Project, ref.Name.Env, app),
		gcloud.Clipped("ocel "+ref.Project+"/"+app+"@"+ref.Name.Env+" ("+string(ref.Tier)+")", gcloud.MaxDisplayNameBytes),
		appAccountDescription(ref.Tier, ref.Project, ref.Name.Env, app))
}

func appAccountDescription(tier environment.Tier, project, env, app string) string {
	return appDescriptionPrefix + app + appDescriptionProject + project + appDescriptionEnvironment + env + appDescriptionTier + string(tier) + appDescriptionSuffix
}
