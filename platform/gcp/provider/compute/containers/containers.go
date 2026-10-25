package containers

import (
	"context"
	"strings"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/functioncode"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/gcp/provider/compute/functions"
	"github.com/ocelhq/ocel/platform/gcp/provider/compute/next"
	"github.com/ocelhq/ocel/platform/gcp/provider/compute/release"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/cloudrun"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
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
	image := functioncode.FindContainerImageRef(spec)
	if strings.TrimSpace(image) == "" {
		return nil, refusal.Refuse(refusal.CodeInvalid,
			"app %s names no image, and a Cloud Run service runs what a registry coordinate names and nothing else", app.App)
	}
	if app.IsBuiltFromFunction() {
		if err := next.RefuseGuardWithoutShieldingEdge(spec); err != nil {
			return nil, err
		}
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
	facts, err := p.ReadEdgeFacts(ctx, spec)
	if err != nil {
		return nil, err
	}
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
	account, databases, err := p.EnsureAccount(ctx, c, spec, declared)
	if err != nil {
		return nil, err
	}
	own, err := p.RuntimeEnv(named, spec, tasks, databases)
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
		Image:     image,
		Env:       values,
		Account:   account,
		Compute:   provider.ComputeContainer,
		Billing:   p.ResolveBilling(app.App, provider.ComputeContainer, next.Serves(app)),
		Health:    app.HealthCheckPath,
		Instances: instances,
		Public:    true,
		Ingress:   run.IngressFor(run.FactsOf(spec.Edge)),
		Egress:    p.EgressFor(named, spec),
		Tag:       p.RevisionTag(spec),
		Labels:    labels,

		OpensOnPromotion: !run.FactsOf(spec.Edge).RunsCode,
	}
	if app.IsBuiltFromFunction() {
		built := functions.Functions{Release: p.Release}
		if err := built.UploadBuildFiles(ctx, spec, progress); err != nil {
			return nil, err
		}
		served = next.FillContainerDefaults(served)
		served.Env, err = built.ServiceEnv(ctx, c, spec, app.Functions[0], served, own, facts)
	} else {
		served, err = p.withImageEnv(ctx, c, spec, served, facts)
	}
	if err != nil {
		return nil, err
	}
	if err := cloudrun.ApplyGate(gated, app.App, &served); err != nil {
		return nil, err
	}
	if err := cloudrun.ApplyProbeToken(ctx, c, app.App, &served); err != nil {
		return nil, err
	}
	ran, err := p.Services.DeployService(ctx, served, progress)
	if err != nil {
		return nil, err
	}
	release.WarnPreviewOpen(spec, service, progress)
	address, err := release.ReadAddress(spec, "container "+app.App, ran)
	if err != nil {
		return nil, err
	}
	deployed := []provider.AppContainer{{
		Name: app.App, Physical: service, URL: address, Image: image, Revision: ran.Revision,
	}}
	workers, err := p.ProvisionWorkers(ctx, c, spec, image, account, values, declared, provider.Instances{Max: budget.Ceiling()}, progress)
	if err != nil {
		return nil, err
	}
	return append(deployed, release.WorkerContainers(workers, image)...), nil
}

func (p Containers) withImageEnv(ctx context.Context, c *gcloud.Clients, spec provider.StackSpec, served run.Serving, facts release.EdgeFacts) (run.Serving, error) {
	app := spec.App
	var err error
	if next.Serves(app) {
		served = next.FillContainerDefaults(served)
		buckets, err := c.TierBuckets(ctx, spec.Ref.Tier, naming.StorageEdge, naming.StorageCache)
		if err != nil {
			return run.Serving{}, err
		}
		database, err := c.Workload().ReadProjectDatabase(ctx, spec.Ref.Tier, spec.Ref.Project)
		if err != nil {
			return run.Serving{}, err
		}
		if served.Env, err = run.MergeValues(app.App, release.NextValues, served.Env, next.NewContainerEnv(spec, served.Memory, next.NewCache(c.Names, buckets, database, p.ContainerEndpoint, p.ContainerTagEndpoint, ""))); err != nil {
			return run.Serving{}, err
		}
	}
	served.Env, err = run.MergeValues(app.App, release.OriginGuardValues, served.Env, release.NewOriginGuardEnv(facts.OriginSecret))
	return served, err
}

func (p Containers) Remove(ctx context.Context, ref provider.StackRef, containers []provider.AppContainer, images provider.ImageStore, progress progress.Log) error {
	if err := p.TearDownAll(ctx, ref, containerRevisions(containers), images, progress); err != nil {
		return err
	}
	c, err := p.OpenClients(ctx)
	if err != nil {
		return err
	}
	return p.DeleteUnusedAccount(ctx, c, ref, nil, containerNames(containers), progress)
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
	image := functioncode.FindContainerImageRef(spec)
	workers := release.WorkerContainers(named, image)
	return append([]provider.AppContainer{{Name: spec.App.App, Physical: service, Image: image}}, workers...), nil
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
