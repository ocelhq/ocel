package release

import (
	"context"
	"errors"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/platform/gcp/provider/assets"
	"github.com/ocelhq/ocel/platform/gcp/provider/component"
	"github.com/ocelhq/ocel/platform/gcp/provider/compute/next"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/cloudrun"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/credential"
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

func (p *Release) EnsureAccount(ctx context.Context, c *gcloud.Clients, spec provider.StackSpec, patches AppPatches, declared map[string]*provider.TopicSpec) (string, map[string]string, error) {
	ref, app := spec.Ref, spec.App.App
	account := c.AppAccount(ref.Project, ref.Name.Env, app)
	identity, err := readAppIdentity(ctx, p.Records, ref.Tier, ref.Project, ref.Name.Env, app)
	if err != nil {
		return "", nil, err
	}
	own := c.AppAccountEmail(ref.Project, ref.Name.Env, app)
	email := patches.Account
	if email == "" {
		email = own
		if err := CreateAppAccount(ctx, c, ref, app); err != nil {
			return "", nil, err
		}
	}
	referenced, err := variables.ReferencedOwners(ctx, p.Records, p.Cipher, spec)
	if err != nil {
		return "", nil, err
	}
	want, err := AppGrants(ctx, c, p.Components, p.CacheGrants, spec, account, "serviceAccount:"+email, declared, referenced)
	if err != nil {
		return "", nil, err
	}
	if patches.Account != "" {
		var owed []grants.Grant
		want, owed = splitOwnAccountGrants(want, account)
		if !p.SkipChecks {
			if err := refuseUnheldIdentity(ctx, c, app, email, owed); err != nil {
				return "", nil, err
			}
		}
	}
	next := identity.switchedTo(patches.Account, own)
	if err := claimRecordedIdentity(ctx, p.Records, ref, app, identity, next); err != nil {
		return "", nil, err
	}
	kept, err := keptByOtherEnvs(ctx, c, p.Records, ref.Tier, ref.Project, ref.Name.Env, app, next.claimed(own)...)
	if err != nil {
		return "", nil, err
	}
	retained, err := grantsOfPrevious(ctx, p.Records, ref.Tier, account, own, next.Previous)
	if err != nil {
		return "", nil, err
	}
	if err := grants.Reconcile(ctx, c, p.Records, ref.Tier, account, want, kept, retained); err != nil {
		return "", nil, err
	}
	databases, err := readProjectDatabases(ctx, c, ref.Tier, append([]string{ref.Project}, referenced...))
	if err != nil {
		return "", nil, err
	}
	return email, databases, nil
}

func claimRecordedIdentity(ctx context.Context, store records.Store, ref provider.StackRef, app string, identity, next appIdentity) error {
	claimed := false
	if next.Account != "" {
		var err error
		if claimed, err = claimAccount(ctx, store, ref, app, next.Account); err != nil {
			return err
		}
	}
	if next.equal(identity) {
		return nil
	}
	err := recordAppIdentity(ctx, store, ref.Tier, ref.Project, ref.Name.Env, app, next)
	if err != nil && claimed {
		return errors.Join(err, releaseAccount(ctx, store, ref.Tier, ref.Project, ref.Name.Env, app, next.Account))
	}
	return err
}

func grantsOfPrevious(ctx context.Context, store records.Store, tier environment.Tier, account, own string, previous []string) ([]grants.Grant, error) {
	if len(previous) == 0 {
		return nil, nil
	}
	recorded, err := grants.Read(ctx, store, tier, account)
	if err != nil {
		return nil, err
	}
	var held []grants.Grant
	for _, email := range previous {
		held = append(held, grantsHeldBy(recorded, email, own, account)...)
	}
	return held, nil
}

func readProjectDatabases(ctx context.Context, c *gcloud.Clients, tier environment.Tier, projects []string) (map[string]string, error) {
	databases := make(map[string]string, len(projects))
	for _, project := range projects {
		database, err := c.Workload().ReadProjectDatabase(ctx, tier, project)
		if err != nil {
			return nil, err
		}
		databases[project] = database
	}
	return databases, nil
}

func AppGrants(ctx context.Context, c *gcloud.Clients, registry component.Registry, cacheGrants CacheGrants, spec provider.StackSpec, account, member string, declared map[string]*provider.TopicSpec, referenced []string) ([]grants.Grant, error) {
	var granted []grants.Grant
	read := map[string]bool{}
	owners := valueOwners(spec, referenced)
	for _, project := range owners {
		database, err := c.Workload().ReadProjectDatabase(ctx, spec.Ref.Tier, project)
		if err != nil {
			return nil, err
		}
		if !read[database] {
			read[database] = true
			granted = append(granted, grants.OnProject(credential.RecordsRole, member, variables.DatabaseCondition(c, database)))
		}
	}
	if len(owners) > 0 {
		granted = append(granted, grants.On(grants.Key, c.Workload().KeyPath(string(spec.Ref.Tier)), grants.OpeningRole, member))
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

func valueOwners(spec provider.StackSpec, referenced []string) []string {
	if len(spec.App.Values.Secrets) == 0 && len(spec.App.Values.Bindings) == 0 {
		return nil
	}
	return append([]string{spec.Ref.Project}, referenced...)
}

func CreateAppAccount(ctx context.Context, c *gcloud.Clients, ref provider.StackRef, app string) error {
	return c.CreateAccount(ctx, ref.Tier, c.AppAccount(ref.Project, ref.Name.Env, app),
		gcloud.Clipped("ocel "+ref.Project+"/"+app+"@"+ref.Name.Env+" ("+string(ref.Tier)+")", gcloud.MaxDisplayNameBytes),
		AppAccountDescription(ref.Tier, ref.Project, ref.Name.Env, app))
}

func AppAccountDescription(tier environment.Tier, project, env, app string) string {
	return appDescriptionPrefix + app + appDescriptionProject + project + appDescriptionEnvironment + env + appDescriptionTier + string(tier) + appDescriptionSuffix
}
