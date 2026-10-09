package gcp

import (
	"context"
	"crypto/rand"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/runtime/live"
	"github.com/ocelhq/ocel/pkg/runtime/originguard"
	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy"
	variables "github.com/ocelhq/ocel/platform/gcp/provider/live"
)

func serviceFor(names Names, spec provider.StackSpec, app *provider.AppSpec, function string) (string, error) {
	return names.Service(spec.Ref.Project, spec.Ref.Name.Env, app.App, function)
}

const rootHealthPath = "/"

func (p *Provider) revisionTag(spec provider.StackSpec) string {
	if !factsOf(spec.Edge).ShieldsOrigin && !p.servesBehindIAP(spec) {
		return ""
	}
	return spec.Ref.Name.Release.String()
}

const previewOpenWarning = "is a preview and answers anyone who knows its Cloud Run url: nothing in front of it shields it, " +
	"and Cloud Run's invoker check would shut browsers out too. Front previews with an edge that shields the origin, or keep their urls to yourselves"

func warnPreviewOpen(spec provider.StackSpec, service string, progress progress.Log) {
	if spec.Ref.Tier != environment.TierPreview || factsOf(spec.Edge).ShieldsOrigin {
		return
	}
	ensureProgress(progress).Warn("Cloud Run service " + service + " " + previewOpenWarning)
}

func (p *Provider) exposeService(ctx context.Context, c *clients, spec provider.StackSpec, service string, progress progress.Log) error {
	if !p.servesBehindIAP(spec) {
		warnPreviewOpen(spec, service, progress)
		return nil
	}
	return p.grantViewers(ctx, c, service, c.AppAccountEmail(spec.Ref.Tier, spec.Ref.Project, spec.App.App), progress)
}

func (p *Provider) ProvisionFunctions(ctx context.Context, spec provider.StackSpec, progress progress.Log) ([]provider.Function, error) {
	app := spec.App
	if app == nil {
		return nil, nil
	}
	if servesNext(app) {
		if err := refuseGuardWithoutShieldingEdge(spec); err != nil {
			return nil, err
		}
	}
	originBase, err := p.readOriginBase(ctx, spec)
	if err != nil {
		return nil, err
	}
	c, err := p.openClients(ctx)
	if err != nil {
		return nil, err
	}
	names := c.Names
	if err := refuseViewersWithoutKind(p.options.PreviewViewers); err != nil {
		return nil, err
	}
	gated := p.servesBehindIAP(spec)
	tasks, declared, err := p.tasksFor(ctx, c, spec)
	if err != nil {
		return nil, err
	}
	account, err := p.ensureAppAccount(ctx, c, spec, declared)
	if err != nil {
		return nil, err
	}
	if err := grantCache(ctx, c, spec, account); err != nil {
		return nil, err
	}
	if gated {
		allowWarming(ctx, c, spec.App.App, account, progress)
	}
	if err := grantCDNPurge(ctx, c, spec, account); err != nil {
		return nil, err
	}
	own, err := p.runtimeEnv(names, spec, tasks)
	if err != nil {
		return nil, err
	}
	edgeStore, err := p.readEdgeISRStore(ctx, spec)
	if err != nil {
		return nil, err
	}
	if servesNext(app) && app.ISR != nil {
		var writer *cloudflare.ISRWriter
		if edgeStore != nil {
			writer = &edgeStore.writer
		}
		if err := p.seedPrerenders(ctx, spec, writer, progress); err != nil {
			return nil, err
		}
	}
	deployed := make([]provider.Function, 0, len(app.Functions)+len(app.Workers))
	var refresh *nextRefresh
	if refreshesByTask(app.Framework, app.Compute, factsOf(spec.Edge), gated) {
		projectNumber, err := c.ReadProjectNumber(ctx)
		if err != nil {
			return nil, err
		}
		refresh = &nextRefresh{
			queue:         names.DelayQueuePath(c.region, spec.Ref.Tier),
			account:       names.RefreshAccountEmail(spec.Ref.Tier),
			secret:        rand.Text(),
			endpoint:      p.containerTasksEndpoint(),
			certsURL:      p.containerTokenCertsURL(),
			projectNumber: projectNumber,
			region:        c.region,
		}
	}
	for _, fn := range app.Functions {
		if err := runsX8664(fn.Framework.Arch, "function "+fn.Name); err != nil {
			return nil, err
		}
		if strings.TrimSpace(fn.Image) == "" {
			return nil, refusal.Refuse(refusal.CodeInvalid,
				"function %s names no image, and a function on Cloud Run is the container a registry coordinate names", fn.Name)
		}
		service, err := serviceFor(names, spec, app, fn.Name)
		if err != nil {
			return nil, err
		}
		values, err := mergedValues(fn.Name, app.Values.ContainerEnv, fn.Env)
		if err != nil {
			return nil, err
		}
		if values, err = mergedValues(fn.Name, values, own); err != nil {
			return nil, err
		}
		served := serving{
			service:          service,
			image:            fn.Image,
			account:          account,
			compute:          provider.ComputeServerless,
			public:           !gated,
			iap:              gated,
			instanceBilled:   servesNext(app),
			ingress:          ingressFor(factsOf(spec.Edge)),
			memory:           fn.Memory,
			egress:           p.egressFor(names, spec),
			tag:              p.revisionTag(spec),
			labels:           labelsFor(names, spec.Ref, appLabel, app.App),
			opensOnPromotion: !factsOf(spec.Edge).RunsCode,
		}
		if servesNext(app) {
			served = fillNextServingDefaults(served)
			var addressed *nextRefresh
			if refresh != nil {
				addressed = refresh.addressedTo(service, served.tag)
			}
			if values, err = mergedValues(fn.Name, values, newNextEnv(spec, fn, served, newNextCache(names, spec.Ref.Tier, p.containerEndpoint(), p.containerTagEndpoint(), edgeStore), addressed)); err != nil {
				return nil, err
			}
			if values, err = mergedValues(fn.Name, values, newCDNPurgeEnv(names, spec)); err != nil {
				return nil, err
			}
		}
		served.env = values
		ran, err := p.deployService(ctx, served, progress)
		if err != nil {
			return nil, err
		}
		if err := p.exposeService(ctx, c, spec, service, progress); err != nil {
			return nil, err
		}
		address := ran.url
		if originBase != "" {
			if served.tag == "" {
				return nil, fmt.Errorf("function %s is served behind the Cloudflare worker, which reaches the revision this release tags, and this release tagged none", fn.Name)
			}
			if address, err = p.routeOriginHost(ctx, spec, originBase, service, served.tag, ran.revision, progress); err != nil {
				return nil, err
			}
		}
		deployed = append(deployed, provider.Function{
			Name: fn.Name, Physical: service, URL: address, DeploymentURL: ran.deploymentURL, Revision: ran.revision,
		})
	}
	if !hostsWorkers(app) {
		return deployed, nil
	}
	values, err := mergedValues(app.App, app.Values.ContainerEnv, own)
	if err != nil {
		return nil, err
	}
	workers, err := p.provisionWorkers(ctx, c, spec, workerImageOf(app), account, values, declared, progress)
	if err != nil {
		return nil, err
	}
	return append(deployed, workerFunctions(workers)...), nil
}

func workerImageOf(app *provider.AppSpec) string {
	if at := slices.IndexFunc(app.Functions, func(fn provider.FunctionSpec) bool { return fn.Name == app.App }); at >= 0 {
		return app.Functions[at].Image
	}
	if len(app.Functions) > 0 {
		return app.Functions[0].Image
	}
	return app.Image
}

func (p *Provider) RemoveFunctions(ctx context.Context, ref provider.StackRef, functions []provider.Function, images provider.ImageStore, progress progress.Log) error {
	if err := p.unrouteServiceOriginHosts(ctx, ref.Tier, functions); err != nil {
		return err
	}
	if err := p.tearDownAll(ctx, ref, functionRevisions(functions), images, progress); err != nil {
		return err
	}
	c, err := p.openClients(ctx)
	if err != nil {
		return err
	}
	return p.revokeAfterRemoval(ctx, c, ref, functionNames(functions), nil, progress)
}

func (p *Provider) NameFunctions(ctx context.Context, spec provider.StackSpec) ([]provider.Function, error) {
	if spec.App == nil {
		return nil, nil
	}
	names, err := p.Names(ctx)
	if err != nil {
		return nil, err
	}
	functions := make([]provider.Function, 0, len(spec.App.Functions)+len(spec.App.Workers))
	for _, fn := range spec.App.Functions {
		service, err := serviceFor(names, spec, spec.App, fn.Name)
		if err != nil {
			return nil, err
		}
		functions = append(functions, provider.Function{Name: fn.Name, Physical: service})
	}
	return append(functions, workerFunctions(nameWorkers(names, spec))...), nil
}

func (p *Provider) RemoveFunctionRevisions(ctx context.Context, ref provider.StackRef, functions []provider.Function, images provider.ImageStore, progress progress.Log) ([]provider.Function, error) {
	kept, err := p.removeRevisions(ctx, ref, functionRevisions(functions), images, progress)
	if err != nil {
		return nil, err
	}
	var left, removed []provider.Function
	for at, function := range functions {
		if slices.Contains(kept, at) {
			left = append(left, function)
			continue
		}
		removed = append(removed, function)
	}
	if err := p.unrouteRevisionOriginHosts(ctx, ref.Tier, removed); err != nil {
		return nil, err
	}
	return left, nil
}

func functionRevisions(functions []provider.Function) []serviceRevision {
	revisions := make([]serviceRevision, 0, len(functions))
	for _, function := range functions {
		revisions = append(revisions, serviceRevision{service: function.Physical, revision: function.Revision})
	}
	return revisions
}

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
	c, err := p.openClients(ctx)
	if err != nil {
		return nil, err
	}
	names := c.Names
	service, err := serviceFor(names, spec, app, app.App)
	if err != nil {
		return nil, err
	}
	if err := refuseViewersWithoutKind(p.options.PreviewViewers); err != nil {
		return nil, err
	}
	gated := p.servesBehindIAP(spec)
	tasks, declared, err := p.tasksFor(ctx, c, spec)
	if err != nil {
		return nil, err
	}
	account, err := p.ensureAppAccount(ctx, c, spec, declared)
	if err != nil {
		return nil, err
	}
	if err := grantCache(ctx, c, spec, account); err != nil {
		return nil, err
	}
	if gated {
		allowWarming(ctx, c, spec.App.App, account, progress)
	}
	own, err := p.runtimeEnv(names, spec, tasks)
	if err != nil {
		return nil, err
	}
	values, err := mergedValues(app.App, app.Values.ContainerEnv, own)
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
		instances: app.Instances,
		public:    !gated,
		iap:       gated,
		ingress:   ingressFor(factsOf(spec.Edge)),
		egress:    p.egressFor(names, spec),
		tag:       p.revisionTag(spec),
		labels:    labelsFor(names, spec.Ref, appLabel, app.App),

		opensOnPromotion: true,
	}
	if servesNext(app) {
		edgeStore, err := p.readEdgeISRStore(ctx, spec)
		if err != nil {
			return nil, err
		}
		served = fillNextContainerDefaults(served)
		if served.env, err = mergedValues(app.App, values, newNextContainerEnv(spec, served.memory, newNextCache(names, spec.Ref.Tier, p.containerEndpoint(), p.containerTagEndpoint(), edgeStore))); err != nil {
			return nil, err
		}
	}
	ran, err := p.deployService(ctx, served, progress)
	if err != nil {
		return nil, err
	}
	if err := p.exposeService(ctx, c, spec, service, progress); err != nil {
		return nil, err
	}
	deployed := []provider.AppContainer{{
		Name: app.App, Physical: service, URL: ran.url, DeploymentURL: ran.deploymentURL, Image: app.Image, Revision: ran.revision,
	}}
	workers, err := p.provisionWorkers(ctx, c, spec, app.Image, account, values, declared, progress)
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

func (p *Provider) revokeAfterRemoval(ctx context.Context, c *clients, ref provider.StackRef, goingFunctions, goingContainers []string, progress progress.Log) error {
	keeps, err := stackKeepsRunning(ctx, p.KeyValues(), ref, goingFunctions, goingContainers)
	if err != nil || keeps {
		return err
	}
	return revokeUnusedAppAccount(ctx, c, p.KeyValues(), ref, progress)
}

func functionNames(functions []provider.Function) []string {
	names := make([]string, 0, len(functions))
	for _, function := range functions {
		names = append(names, function.Name)
	}
	return names
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

type serviceRevision struct {
	service  string
	revision string
}

func (p *Provider) tearDownAll(ctx context.Context, ref provider.StackRef, going []serviceRevision, images provider.ImageStore, progress progress.Log) error {
	var ran []string
	defer func() { p.removeUnusedImages(ctx, ref, ran, images, progress) }()
	for _, each := range going {
		if each.service == "" {
			continue
		}
		images, err := p.deleteService(ctx, each.service, progress)
		if err != nil {
			return err
		}
		ran = append(ran, images...)
	}
	return nil
}

func (p *Provider) removeRevisions(ctx context.Context, ref provider.StackRef, going []serviceRevision, images provider.ImageStore, progress progress.Log) ([]int, error) {
	var (
		kept []int
		ran  []string
	)
	defer func() { p.removeUnusedImages(ctx, ref, ran, images, progress) }()
	for at, each := range going {
		stays, images, err := p.removeRevision(ctx, each.service, each.revision, progress)
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

func (p *Provider) egressFor(names Names, spec provider.StackSpec) *privateEgress {
	if !slices.ContainsFunc(spec.App.Values.Bindings, func(binding provider.Binding) bool {
		return binding.Type == provider.BindingKV || binding.Type == provider.BindingPostgres
	}) {
		return nil
	}
	return &privateEgress{network: names.NetworkPath(spec.Ref.Tier), subnetwork: names.SubnetworkPath(p.options.Region, spec.Ref.Tier)}
}

func (p *Provider) runtimeEnv(names Names, spec provider.StackSpec, tasks *variables.Tasks) (map[string]string, error) {
	app := spec.App
	env := map[string]string{}
	if app.HealthCheckPath != "" {
		env[originguard.HealthPathVar] = app.HealthCheckPath
	}
	manifest, err := variables.Render(variables.Manifest{
		Project:            names.project,
		Region:             p.options.Region,
		Namespace:          string(names.namespace),
		Slug:               spec.Ref.Project,
		Tier:               string(spec.Ref.Tier),
		Environment:        liveEnvironment(spec.Ref),
		Endpoint:           p.containerEndpoint(),
		Keys:               liveKeys(app.Values),
		Bindings:           liveBindings(app.Values),
		Tasks:              tasks,
		RealtimePublishURL: findRealtimePublishURL(app.Values.Bindings),
	})
	if err != nil {
		return nil, fmt.Errorf("pin %s's live values: %w", app.App, err)
	}
	if len(manifest) > 0 {
		env[variables.EnvVar] = string(manifest)
	}
	return env, nil
}

func liveEnvironment(ref provider.StackRef) string {
	if ref.Tier == environment.TierProduction {
		return ""
	}
	return ref.Name.Env
}

func liveKeys(values provider.AppValues) []live.Key {
	keys := make([]live.Key, 0, len(values.Secrets))
	for _, secret := range values.Secrets {
		keys = append(keys, live.Key{Key: secret.Key, Folder: secret.Folder})
	}
	return keys
}

func liveBindings(values provider.AppValues) []live.Binding {
	bindings := make([]live.Binding, 0, len(values.Bindings))
	for _, binding := range values.Bindings {
		kind := provider.ProtoBindingType(binding.Type)
		resource := binding.Resource
		if resource == "" {
			resource = binding.Name
		}
		bindings = append(bindings, live.Binding{
			Name:    binding.Name,
			Key:     naming.ResourceEnvName(kind, resource),
			Type:    kind,
			Granted: binding.Version,
		})
	}
	return bindings
}

func mergedValues(what string, delivered, own map[string]string) (map[string]string, error) {
	values := make(map[string]string, len(delivered)+len(own))
	maps.Copy(values, delivered)
	for _, name := range slices.Sorted(maps.Keys(own)) {
		if _, taken := values[name]; taken {
			return nil, refusal.Refuse(refusal.CodeInvalid,
				"%s sets %s in the environment its own spec names, and the deploy already resolved a value for %s: "+
					"a revision has one entry per name, so the spec's would silently take the place of what the deploy delivered "+
					"and the app would read a value nothing in it declared. Rename one of them",
				what, name, name)
		}
		values[name] = own[name]
	}
	return values, nil
}

func (p *Provider) readEdgeISRWriter(ctx context.Context, spec provider.StackSpec) (cloudflare.ISRWriter, error) {
	app := spec.App
	if app == nil || app.ISR == nil || app.Compute != provider.ComputeServerless || !factsOf(spec.Edge).RunsCode {
		return cloudflare.ISRWriter{}, nil
	}
	c, err := p.openClients(ctx)
	if err != nil {
		return cloudflare.ISRWriter{}, err
	}
	adopted, credentials, err := requireAdoptedEdge(ctx, c, p.KeyValues(), spec.Ref.Tier, spec.Edge.Kind())
	if err != nil {
		return cloudflare.ISRWriter{}, err
	}
	seed, err := readISRWriterSeed(ctx, c, spec.Ref.Tier, spec.Edge.Kind())
	if err != nil {
		return cloudflare.ISRWriter{}, err
	}
	return cloudflare.ISRWriter{Endpoint: adopted.ISRWriter.Endpoint, BootstrapCredential: credentials.ISRWriter, Seed: seed}, nil
}

type stacks struct {
	provider.Stacks
	p *Provider
}

func (s stacks) Provision(ctx context.Context, spec provider.StackSpec, runProgress progress.Log) (provider.StackResult, error) {
	if _, err := s.p.readOriginBase(ctx, spec); err != nil {
		return provider.StackResult{}, err
	}
	writer, err := s.p.readEdgeISRWriter(ctx, spec)
	if err != nil {
		return provider.StackResult{}, err
	}
	if writer.IsConfigured() {
		if err := writer.Initialize(ctx, spec.App.ISR.Prefix); err != nil {
			return provider.StackResult{}, fmt.Errorf("register %s's ISR prefix with the %s edge's writer: %w", spec.App.App, spec.Edge.Kind(), err)
		}
	}
	routeTableKey, err := s.p.putEdgeRouteTable(ctx, spec, runProgress)
	if err != nil {
		return provider.StackResult{}, err
	}
	result, err := s.Stacks.Provision(ctx, spec, runProgress)
	if err != nil {
		return result, err
	}
	result.RouteTableKey = routeTableKey
	if writer.IsConfigured() {
		result.ISRWriteSecret = cloudflare.DeriveISRWriteSecret(writer.Seed, spec.App.ISR.Prefix)
	}
	return result, nil
}

func (p *Provider) readEdgeISRStore(ctx context.Context, spec provider.StackSpec) (*edgeISRStore, error) {
	if !keepsISRInEdgeStore(spec) {
		return nil, nil
	}
	writer, err := p.readEdgeISRWriter(ctx, spec)
	if err != nil {
		return nil, err
	}
	if !writer.IsConfigured() {
		return nil, refusal.Refuse(refusal.CodeNotReady,
			"the %s edge's ISR writer is not adopted, and %s keeps its pages and tags in it: run `%s` again",
			spec.Edge.Kind(), spec.App.App, provider.BootstrapCommand(spec.Ref.Tier))
	}
	return &edgeISRStore{writer: writer, secret: cloudflare.DeriveISRWriteSecret(writer.Seed, spec.App.ISR.Prefix)}, nil
}
