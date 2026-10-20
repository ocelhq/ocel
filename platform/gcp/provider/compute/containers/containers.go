package containers

import (
	"context"
	"strings"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/gcp/provider/compute/next"
	"github.com/ocelhq/ocel/platform/gcp/provider/compute/release"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/cloudrun"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/names"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/run"
	"github.com/ocelhq/ocel/platform/gcp/provider/resources/postgres"
)

const rootHealthPath = "/"

type Containers struct{ *release.Release }

func (p Containers) Provision(ctx context.Context, spec provider.StackSpec, progress progress.Log) ([]provider.AppContainer, error) {
	app := spec.App
	if app == nil {
		return nil, nil
	}
	if strings.TrimSpace(app.Image) == "" {
		return nil, refusal.Refuse(refusal.CodeInvalid,
			"app %s names no image, and a Cloud Run service runs what a registry coordinate names and nothing else", app.App)
	}
	if strings.TrimSpace(app.HealthCheckPath) == "" {
		probed := *app
		probed.HealthCheckPath = rootHealthPath
		spec.App, app = &probed, &probed
	}
	budget := postgres.ConnectionBudgetOf(spec, 1+len(app.Workers))
	instances, err := budget.InstancesFor(app.Instances)
	if err != nil {
		return nil, err
	}
	budget.WarnOversubscribed(progress)
	c, err := p.OpenClients(ctx)
	if err != nil {
		return nil, err
	}
	named := c.Names
	service, err := release.NameService(named, spec, app, app.App)
	if err != nil {
		return nil, err
	}
	tasks, declared, err := p.Topics.Tasks(ctx, c, spec)
	if err != nil {
		return nil, err
	}
	gated, err := cloudrun.ProvisionGate(ctx, c, spec, p.GateInEnv)
	if err != nil {
		return nil, err
	}
	account, err := p.EnsureAccount(ctx, c, spec, declared)
	if err != nil {
		return nil, err
	}
	own, err := p.RuntimeEnv(named, spec, tasks)
	if err != nil {
		return nil, err
	}
	labels, err := names.LabelsFor(named, spec.Ref, names.AppLabel, app.App)
	if err != nil {
		return nil, err
	}
	values, err := run.MergeValues(app.App, release.RuntimeValues, app.Values.ContainerEnv, own)
	if err != nil {
		return nil, err
	}
	served := run.Serving{
		Service:   service,
		Tier:      spec.Ref.Tier,
		Image:     app.Image,
		Env:       values,
		Account:   account,
		Compute:   provider.ComputeContainer,
		Health:    app.HealthCheckPath,
		Instances: instances,
		Public:    true,
		Ingress:   run.IngressFor(run.FactsOf(spec.Edge)),
		Egress:    p.EgressFor(named, spec),
		Tag:       p.RevisionTag(spec),
		Labels:    labels,

		OpensOnPromotion: true,
	}
	if next.Serves(app) {
		served = next.FillContainerDefaults(served)
		buckets, err := c.TierBuckets(ctx, spec.Ref.Tier, naming.StorageEdge, naming.StorageCache)
		if err != nil {
			return nil, err
		}
		if served.Env, err = run.MergeValues(app.App, release.NextValues, values, next.NewContainerEnv(spec, served.Memory, next.NewCache(named, buckets, spec.Ref, p.ContainerEndpoint, p.ContainerTagEndpoint, ""))); err != nil {
			return nil, err
		}
	}
	if err := cloudrun.ApplyGate(ctx, c, gated, app.App, &served); err != nil {
		return nil, err
	}
	ran, err := p.Services.DeployService(ctx, served, progress)
	if err != nil {
		return nil, err
	}
	release.WarnPreviewOpen(spec, service, progress)
	deployed := []provider.AppContainer{{
		Name: app.App, Physical: service, URL: ran.Url, Image: app.Image, Revision: ran.Revision,
	}}
	workers, err := p.ProvisionWorkers(ctx, c, spec, app.Image, account, values, declared, provider.Instances{Max: budget.Ceiling()}, progress)
	if err != nil {
		return nil, err
	}
	return append(deployed, release.WorkerContainers(workers, app.Image)...), nil
}

func (p Containers) Remove(ctx context.Context, ref provider.StackRef, containers []provider.AppContainer, images provider.ImageStore, progress progress.Log) error {
	if err := p.TearDownAll(ctx, ref, containerRevisions(containers), images, progress); err != nil {
		return err
	}
	c, err := p.OpenClients(ctx)
	if err != nil {
		return err
	}
	return p.RevokeAfterRemoval(ctx, c, ref, nil, containerNames(containers), progress)
}

func containerNames(containers []provider.AppContainer) []string {
	names := make([]string, 0, len(containers))
	for _, container := range containers {
		names = append(names, container.Name)
	}
	return names
}

func (p Containers) Name(ctx context.Context, spec provider.StackSpec) ([]provider.AppContainer, error) {
	if spec.App == nil {
		return nil, nil
	}
	names, err := p.ReadNames(ctx)
	if err != nil {
		return nil, err
	}
	service, err := release.NameService(names, spec, spec.App, spec.App.App)
	if err != nil {
		return nil, err
	}
	named, err := release.NameWorkers(names, spec)
	if err != nil {
		return nil, err
	}
	workers := release.WorkerContainers(named, spec.App.Image)
	return append([]provider.AppContainer{{Name: spec.App.App, Physical: service, Image: spec.App.Image}}, workers...), nil
}

func (p Containers) RemoveRevisions(ctx context.Context, ref provider.StackRef, containers []provider.AppContainer, images provider.ImageStore, progress progress.Log) ([]provider.AppContainer, error) {
	kept, err := p.Release.RemoveRevisions(ctx, ref, containerRevisions(containers), images, progress)
	if err != nil {
		return nil, err
	}
	var left []provider.AppContainer
	for _, at := range kept {
		left = append(left, containers[at])
	}
	return left, nil
}

func containerRevisions(containers []provider.AppContainer) []release.ServiceRevision {
	revisions := make([]release.ServiceRevision, 0, len(containers))
	for _, container := range containers {
		revisions = append(revisions, release.ServiceRevision{Service: container.Physical, Revision: container.Revision})
	}
	return revisions
}
