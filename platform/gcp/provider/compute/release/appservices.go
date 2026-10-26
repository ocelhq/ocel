package release

import (
	"cmp"
	"context"
	"fmt"
	"slices"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/cloudrun"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/credential"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/grants"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/names"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/run"
)

func NameService(names names.Names, spec provider.StackSpec, app *provider.AppSpec, function string) (string, error) {
	return names.Service(spec.Ref.Project, spec.Ref.Name.Env, app.App, function)
}

func (p *Release) RevisionTag(spec provider.StackSpec) string {
	if !run.FactsOf(spec.Edge).ShieldsOrigin {
		return ""
	}
	return spec.Ref.Name.Release.String()
}

const previewOpenWarning = "is a preview and answers anyone who knows its Cloud Run url: nothing in front of it shields it and protection.preview is off, so no gate checks its requests. " +
	"Turn protection.preview on, front previews with an edge that shields the origin, or keep their urls to yourselves"

func WarnPreviewOpen(spec provider.StackSpec, service string, progress progress.Log) {
	if spec.Ref.Tier != environment.TierPreview || run.FactsOf(spec.Edge).ShieldsOrigin || cloudrun.IsGated(spec) {
		return
	}
	gcloud.EnsureProgress(progress).Warn("Cloud Run service " + service + " " + previewOpenWarning)
}

func (p *Release) DeleteUnusedAccount(ctx context.Context, c *gcloud.Clients, ref provider.StackRef, goingFunctions, goingContainers []string, progress progress.Log) error {
	keeps, err := StackKeepsRunning(ctx, p.Records, ref, goingFunctions, goingContainers)
	if err != nil || keeps {
		return err
	}
	if ref.Name.IsInfra() {
		return nil
	}
	recorded, err := stackrecords.List(ctx, p.Records, ref.Tier, ref.Project)
	if err != nil {
		return err
	}
	if runsAppElsewhere(recorded, ref) {
		return p.forgetUnusedPreviousAccounts(ctx, c, ref, recorded, progress)
	}
	account := c.AppAccount(ref.Project, ref.Name.Env, ref.Name.App)
	revoked, ranAs, err := RevokeAppGrants(ctx, c, p.Records, ref.Tier, ref.Project, ref.Name.Env, ref.Name.App)
	if err != nil {
		return err
	}
	if recorded, err = stackrecords.List(ctx, p.Records, ref.Tier, ref.Project); err != nil {
		return err
	}
	if runsAppElsewhere(recorded, ref) {
		return nil
	}
	deleted, err := c.DeleteAccount(ctx, ref.Tier, account)
	if err != nil {
		return gcloud.ExplainMissingGrant(err, c.DeployCredential(ref.Tier).Tiered(credential.AccountsRole).String())
	}
	switch {
	case deleted:
		gcloud.EnsureProgress(progress).Say(fmt.Sprintf("Deleted the %s service account: nothing runs as app %s of %s in environment %s any more", account, ref.Name.App, ref.Project, ref.Name.Env))
	case revoked:
		gcloud.EnsureProgress(progress).Say(fmt.Sprintf("Revoked what app %s of %s was granted in environment %s: nothing runs as the %s service account any more", ref.Name.App, ref.Project, ref.Name.Env, cmp.Or(ranAs, account)))
	}
	return nil
}

func RevokeAppGrants(ctx context.Context, c *gcloud.Clients, store records.Store, tier environment.Tier, project, env, app string) (bool, string, error) {
	identity, err := readAppIdentity(ctx, store, tier, project, env, app)
	if err != nil {
		return false, "", err
	}
	claimed := identity.claimed(c.AppAccountEmail(project, env, app))
	kept, err := keptByOtherEnvs(ctx, c, store, tier, project, env, app, claimed...)
	if err != nil {
		return false, "", err
	}
	revoked, err := grants.RevokeAll(ctx, c, store, tier, c.AppAccount(project, env, app), kept)
	if err != nil {
		return revoked, identity.Account, err
	}
	for _, account := range claimed {
		if err := releaseAccount(ctx, store, tier, project, env, app, account); err != nil {
			return revoked, identity.Account, err
		}
	}
	return revoked, identity.Account, recordAppIdentity(ctx, store, tier, project, env, app, appIdentity{})
}

func (p *Release) forgetUnusedPreviousAccounts(ctx context.Context, c *gcloud.Clients, ref provider.StackRef, recorded []stackrecords.NamedStack, progress progress.Log) error {
	tier, project, env, app := ref.Tier, ref.Project, ref.Name.Env, ref.Name.App
	identity, err := readAppIdentity(ctx, p.Records, tier, project, env, app)
	if err != nil {
		return err
	}
	current := cmp.Or(identity.Account, c.AppAccountEmail(project, env, app))
	for _, previous := range identity.Previous {
		used, err := p.isRunByAnotherRelease(ctx, ref, recorded, previous)
		if err != nil {
			return err
		}
		if used {
			continue
		}
		if err := p.forgetAccount(ctx, c, ref, previous, progress); err != nil {
			return err
		}
		if err := forgetPreviousIdentity(ctx, p.Records, tier, project, env, app, previous); err != nil {
			return err
		}
	}
	used, err := p.isRunByAnotherRelease(ctx, ref, recorded, current)
	if err != nil || used {
		return err
	}
	if err := p.forgetAccount(ctx, c, ref, current, progress); err != nil {
		return err
	}
	return forgetCurrentIdentity(ctx, p.Records, tier, project, env, app, identity.Account, c.AppAccountEmail(project, env, app))
}

func (p *Release) isRunByAnotherRelease(ctx context.Context, ref provider.StackRef, recorded []stackrecords.NamedStack, email string) (bool, error) {
	for _, stack := range recorded {
		if stack.Name == ref.Name || stack.Name.IsInfra() || stack.Name.Env != ref.Name.Env || stack.Name.App != ref.Name.App {
			continue
		}
		runs, err := p.isRunningAs(ctx, stack.Stack, email)
		if err != nil || runs {
			return runs, err
		}
	}
	return false, nil
}

func (p *Release) forgetAccount(ctx context.Context, c *gcloud.Clients, ref provider.StackRef, unused string, progress progress.Log) error {
	tier, project, env, app := ref.Tier, ref.Project, ref.Name.Env, ref.Name.App
	account, own := c.AppAccount(project, env, app), c.AppAccountEmail(project, env, app)
	recorded, err := grants.Read(ctx, p.Records, tier, account)
	if err != nil {
		return err
	}
	var kept []grants.Grant
	if unused != own {
		if kept, err = keptByOtherEnvs(ctx, c, p.Records, tier, project, env, app, unused); err != nil {
			return err
		}
	}
	if err := grants.Revoke(ctx, c, p.Records, tier, account, grantsHeldBy(recorded, unused, own, account), kept); err != nil {
		return err
	}
	if unused == own {
		return p.deleteOwnAccount(ctx, c, ref, progress)
	}
	return releaseAccount(ctx, p.Records, tier, project, env, app, unused)
}

func (p *Release) deleteOwnAccount(ctx context.Context, c *gcloud.Clients, ref provider.StackRef, progress progress.Log) error {
	account := c.AppAccount(ref.Project, ref.Name.Env, ref.Name.App)
	exists, err := c.AccountExists(ctx, account)
	if err != nil || !exists {
		return err
	}
	deleted, err := c.DeleteAccount(ctx, ref.Tier, account)
	if err != nil {
		return gcloud.ExplainMissingGrant(err, c.DeployCredential(ref.Tier).Tiered(credential.AccountsRole).String())
	}
	if deleted {
		gcloud.EnsureProgress(progress).Say(fmt.Sprintf("Deleted the %s service account: no release of app %s of %s runs as it in environment %s any more", account, ref.Name.App, ref.Project, ref.Name.Env))
	}
	return nil
}

func (p *Release) isRunningAs(ctx context.Context, stack stackrecords.Stack, email string) (bool, error) {
	var revisions []ServiceRevision
	for _, function := range stack.Functions {
		revisions = append(revisions, ServiceRevision{Service: function.Physical, Revision: function.Revision})
	}
	for _, container := range stack.Containers {
		revisions = append(revisions, ServiceRevision{Service: container.Physical, Revision: container.Revision})
	}
	for _, each := range revisions {
		if each.Service == "" || each.Revision == "" {
			continue
		}
		account, found, err := p.Services.ReadRevisionAccount(ctx, each.Service, each.Revision)
		if err != nil || found {
			return account == email, err
		}
	}
	return len(revisions) == 0, nil
}

func runsAppElsewhere(recorded []stackrecords.NamedStack, ref provider.StackRef) bool {
	return slices.ContainsFunc(recorded, func(stack stackrecords.NamedStack) bool {
		return stack.Name != ref.Name && !stack.Name.IsInfra() && stack.Name.Env == ref.Name.Env && stack.Name.App == ref.Name.App
	})
}

func StackKeepsRunning(ctx context.Context, store records.Store, ref provider.StackRef, goingFunctions, goingContainers []string) (bool, error) {
	recorded, found, err := stackrecords.Read(ctx, store, ref.Tier, ref.Project, ref.Name)
	if err != nil || !found {
		return false, err
	}
	return slices.ContainsFunc(recorded.Functions, func(function provider.Function) bool {
		return !slices.Contains(goingFunctions, function.Name)
	}) || slices.ContainsFunc(recorded.Containers, func(container provider.AppContainer) bool {
		return !slices.Contains(goingContainers, container.Name)
	}), nil
}

type ServiceRevision struct {
	Service  string
	Revision string
}

func (p *Release) TearDownAll(ctx context.Context, ref provider.StackRef, going []ServiceRevision, images provider.ImageStore, progress progress.Log) error {
	var ran []string
	defer func() { p.Services.RemoveUnusedImages(ctx, ref, ran, images, progress) }()
	for _, each := range going {
		if each.Service == "" {
			continue
		}
		images, err := p.Services.DeleteService(ctx, each.Service, progress)
		if err != nil {
			return err
		}
		ran = append(ran, images...)
	}
	return nil
}

func (p *Release) RemoveRevisions(ctx context.Context, ref provider.StackRef, going []ServiceRevision, images provider.ImageStore, progress progress.Log) ([]int, error) {
	var (
		kept []int
		ran  []string
	)
	defer func() { p.Services.RemoveUnusedImages(ctx, ref, ran, images, progress) }()
	for at, each := range going {
		stays, images, err := p.Services.RemoveRevision(ctx, each.Service, each.Revision, progress)
		if err != nil {
			return nil, err
		}
		ran = append(ran, images...)
		if stays {
			kept = append(kept, at)
		}
	}
	return kept, nil
}

func (p *Release) EgressFor(names names.Names, spec provider.StackSpec) *run.PrivateEgress {
	if !p.Components.Egress(spec.App.Values.Bindings) {
		return nil
	}
	return &run.PrivateEgress{Network: names.NetworkPath(spec.Ref.Tier), Subnetwork: names.SubnetworkPath(p.Region, spec.Ref.Tier)}
}
