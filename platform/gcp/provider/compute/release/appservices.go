package release

import (
	"context"
	"fmt"
	"slices"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/cloudrun"
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

func (p *Release) RevokeAfterRemoval(ctx context.Context, c *gcloud.Clients, ref provider.StackRef, goingFunctions, goingContainers []string, progress progress.Log) error {
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
		return nil
	}
	account := c.AppAccount(ref.Project, ref.Name.Env, ref.Name.App)
	revoked, err := grants.RevokeAll(ctx, c, p.Records, ref.Tier, account)
	if err != nil {
		return err
	}
	if err := removeValueSecrets(ctx, c, p.Records, ref.Tier, account); err != nil {
		return err
	}
	if revoked {
		gcloud.EnsureProgress(progress).Say(fmt.Sprintf("Revoked what app %s of %s was granted in environment %s: nothing runs as the %s service account any more", ref.Name.App, ref.Project, ref.Name.Env, account))
	}
	return nil
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
	if len(kept) == len(going) {
		return kept, nil
	}
	if err := p.destroyUnpinnedVersions(ctx, ref); err != nil {
		gcloud.EnsureProgress(progress).Warn(fmt.Sprintf("Left the older versions of app %s's values in environment %s in place, and the next release that retires one tries again: %v", ref.Name.App, ref.Name.Env, err))
	}
	return kept, nil
}

func (p *Release) EgressFor(names names.Names, spec provider.StackSpec) *run.PrivateEgress {
	if !p.Components.Egress(spec.App.Values.Bindings) {
		return nil
	}
	return &run.PrivateEgress{Network: names.NetworkPath(spec.Ref.Tier), Subnetwork: names.SubnetworkPath(p.Region, spec.Ref.Tier)}
}
