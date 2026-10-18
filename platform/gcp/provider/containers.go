package gcp

import (
	"context"
	"strings"

	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
)

const rootHealthPath = "/"

func (p *Provider) ProvisionContainers(ctx context.Context, spec provider.StackSpec, progress progress.Log) ([]provider.AppContainer, error) {
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
	names := c.Names
	service, err := serviceFor(names, spec, app, app.App)
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
	own, err := p.runtimeEnv(names, spec, tasks)
	if err != nil {
		return nil, err
	}
	values, err := mergeValues(app.App, runtimeValues, app.Values.ContainerEnv, own)
	if err != nil {
		return nil, err
	}
	served := serving{
		service:   service,
		image:     app.Image,
		env:       values,
		account:   account,
		compute:   provider.ComputeContainer,
		health:    app.HealthCheckPath,
		instances: instances,
		public:    true,
		ingress:   ingressFor(factsOf(spec.Edge)),
		egress:    p.egressFor(names, spec),
		tag:       p.revisionTag(spec),
		labels:    labelsFor(names, spec.Ref, appLabel, app.App),

		opensOnPromotion: true,
	}
	if servesNext(app) {
		served = fillNextContainerDefaults(served)
		if served.env, err = mergeValues(app.App, nextValues, values, newNextContainerEnv(spec, served.memory, newNextCache(names, spec.Ref.Tier, p.containerEndpoint(), p.containerTagEndpoint(), ""))); err != nil {
			return nil, err
		}
	}
	if err := c.applyGate(ctx, gated, app.App, &served); err != nil {
		return nil, err
	}
	ran, err := p.deployService(ctx, served, progress)
	if err != nil {
		return nil, err
	}
	warnPreviewOpen(spec, service, progress)
	deployed := []provider.AppContainer{{
		Name: app.App, Physical: service, URL: ran.url, Image: app.Image, Revision: ran.revision,
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
