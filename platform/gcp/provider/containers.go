package gcp

import (
	"context"
	"strings"

	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/names"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/run"
)

const rootHealthPath = "/"

func (p *Provider) ProvisionContainers(ctx context.Context, spec provider.StackSpec, progress progress.Log) ([]provider.AppContainer, error) {
	app := spec.App
	if app == nil {
		return nil, nil
	}
	if strings.TrimSpace(app.Image) == "" {
		return nil, refusal.Refuse(refusal.CodeInvalid,
			"app %s named no image, and a Cloud Run service runs what a registry coordinate named and nothing else", app.App)
	}
	if strings.TrimSpace(app.HealthCheckPath) == "" {
		probed := *app
		probed.HealthCheckPath = rootHealthPath
		spec.App, app = &probed, &probed
	}
	budget := connectionBudgetOf(spec, 1+len(app.Workers))
	instances, err := budget.instancesFor(app.Instances)
	if err != nil {
		return nil, err
	}
	budget.warnOversubscribed(progress)
	c, err := p.openClients(ctx)
	if err != nil {
		return nil, err
	}
	named := c.Names
	service, err := serviceFor(named, spec, app, app.App)
	if err != nil {
		return nil, err
	}
	tasks, declared, err := p.tasksFor(ctx, c, spec)
	if err != nil {
		return nil, err
	}
	gated, err := p.provisionGate(ctx, c, spec)
	if err != nil {
		return nil, err
	}
	account, err := p.ensureAppAccount(ctx, c, spec, declared)
	if err != nil {
		return nil, err
	}
	own, err := p.runtimeEnv(named, spec, tasks)
	if err != nil {
		return nil, err
	}
	values, err := mergeValues(app.App, runtimeValues, app.Values.ContainerEnv, own)
	if err != nil {
		return nil, err
	}
	served := run.Serving{
		Service:   service,
		Image:     app.Image,
		Env:       values,
		Account:   account,
		Compute:   provider.ComputeContainer,
		Health:    app.HealthCheckPath,
		Instances: instances,
		Public:    true,
		Ingress:   run.IngressFor(run.FactsOf(spec.Edge)),
		Egress:    p.egressFor(named, spec),
		Tag:       p.revisionTag(spec),
		Labels:    names.LabelsFor(named, spec.Ref, names.AppLabel, app.App),

		OpensOnPromotion: true,
	}
	if servesNext(app) {
		served = fillNextContainerDefaults(served)
		if served.Env, err = mergeValues(app.App, nextValues, values, newNextContainerEnv(spec, served.Memory, newNextCache(named, spec.Ref.Tier, p.containerEndpoint(), p.containerTagEndpoint(), ""))); err != nil {
			return nil, err
		}
	}
	if err := applyGate(ctx, c, gated, app.App, &served); err != nil {
		return nil, err
	}
	ran, err := p.services().DeployService(ctx, served, progress)
	if err != nil {
		return nil, err
	}
	warnPreviewOpen(spec, service, progress)
	deployed := []provider.AppContainer{{
		Name: app.App, Physical: service, URL: ran.Url, Image: app.Image, Revision: ran.Revision,
	}}
	workers, err := p.provisionWorkers(ctx, c, spec, app.Image, account, values, declared, provider.Instances{Max: budget.ceiling()}, progress)
	if err != nil {
		return nil, err
	}
	return append(deployed, workerContainers(workers, app.Image)...), nil
}

func (p *Provider) RemoveContainers(ctx context.Context, ref provider.StackRef, containers []provider.AppContainer, images provider.ImageStore, progress progress.Log) error {
	if err := p.tearDownAll(ctx, ref, containerRevisions(containers), images, progress); err != nil {
		return err
	}
	c, err := p.openClients(ctx)
	if err != nil {
		return err
	}
	return p.revokeAfterRemoval(ctx, c, ref, nil, containerNames(containers), progress)
}

func containerNames(containers []provider.AppContainer) []string {
	names := make([]string, 0, len(containers))
	for _, container := range containers {
		names = append(names, container.Name)
	}
	return names
}

func (p *Provider) NameContainers(ctx context.Context, spec provider.StackSpec) ([]provider.AppContainer, error) {
	if spec.App == nil {
		return nil, nil
	}
	names, err := p.Names(ctx)
	if err != nil {
		return nil, err
	}
	service, err := serviceFor(names, spec, spec.App, spec.App.App)
	if err != nil {
		return nil, err
	}
	workers := workerContainers(nameWorkers(names, spec), spec.App.Image)
	return append([]provider.AppContainer{{Name: spec.App.App, Physical: service, Image: spec.App.Image}}, workers...), nil
}

func (p *Provider) RemoveContainerRevisions(ctx context.Context, ref provider.StackRef, containers []provider.AppContainer, images provider.ImageStore, progress progress.Log) ([]provider.AppContainer, error) {
	kept, err := p.removeRevisions(ctx, ref, containerRevisions(containers), images, progress)
	if err != nil {
		return nil, err
	}
	var left []provider.AppContainer
	for _, at := range kept {
		left = append(left, containers[at])
	}
	return left, nil
}

func containerRevisions(containers []provider.AppContainer) []serviceRevision {
	revisions := make([]serviceRevision, 0, len(containers))
	for _, container := range containers {
		revisions = append(revisions, serviceRevision{service: container.Physical, revision: container.Revision})
	}
	return revisions
}
