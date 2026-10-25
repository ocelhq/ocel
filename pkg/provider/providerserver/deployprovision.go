package providerserver

import (
	"cmp"
	"context"
	"net/url"
	"slices"
	"strings"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/progress"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

func (r *deployRun) provision(ctx context.Context) error {
	if err := r.reconcileInfra(ctx, nil); err != nil {
		return err
	}
	return r.provisionApps(ctx)
}

func (r *deployRun) reconcileInfra(ctx context.Context, undeclared []*contractv1.ManifestResource) error {
	if r.infraCurrent {
		return r.mode.currentInfra(ctx, r)
	}
	return r.provisionInfra(ctx, undeclared)
}

const appConcurrency = 4

type appProvisioned struct {
	provisioning bool
	address      string
	deployment   edge.PreviewHost
	notes        []string
}

func (r *deployRun) provisionApps(ctx context.Context) error {
	failures := make([]error, len(r.spec.Apps))
	provisioned := make([]appProvisioned, len(r.spec.Apps))
	var apps errgroup.Group
	apps.SetLimit(appConcurrency)
	for slot, entry := range r.spec.Apps {
		apps.Go(func() error {
			provisioned[slot], failures[slot] = r.provisionApp(ctx, slot, entry)
			return nil
		})
	}
	_ = apps.Wait()
	r.provisioned = provisioned
	var first error
	for slot, err := range failures {
		r.outcomes[slot] = appOutcome(r.spec.Apps[slot].App, err)
		if host := provisioned[slot].deployment; host.Hostname != "" {
			r.deployment = append(r.deployment, host)
		}
		r.pending = append(r.pending, provisioned[slot].notes...)
		if err != nil && first == nil {
			first = err
		}
	}
	return first
}

func appOutcome(app string, err error) *progressv1.AppResult {
	if err != nil {
		return &progressv1.AppResult{
			App:     app,
			Outcome: progressv1.AppOutcome_APP_OUTCOME_FAILED,
			Error:   err.Error(),
		}
	}
	return &progressv1.AppResult{App: app, Outcome: progressv1.AppOutcome_APP_OUTCOME_SUCCEEDED}
}

func (r *deployRun) provisionInfra(ctx context.Context, undeclared []*contractv1.ManifestResource) error {
	return r.spanEvents.run(r.spans.Infra, func(u *spanRun) error {
		return u.phase(func(progress progress.Log) error {
			stack, held, err := r.infraStack(ctx, undeclared)
			if err != nil {
				return err
			}
			return r.mode.infra(ctx, r, stack, held, progress)
		})
	})
}

func (r *deployRun) infraStack(ctx context.Context, undeclared []*contractv1.ManifestResource) (provider.StackSpec, infraHeld, error) {
	declaredResources, err := manifestResources(r.manifest)
	if err != nil {
		return provider.StackSpec{}, infraHeld{}, err
	}
	undeclaredNames := map[string]bool{}
	for _, held := range undeclared {
		undeclaredNames[resourceName(held)] = true
	}
	var held []*contractv1.ManifestResource
	var resources []provider.Resource
	for i, resource := range r.manifest.GetResources() {
		if !undeclaredNames[resourceName(resource)] {
			held = append(held, resource)
			resources = append(resources, declaredResources[i])
		}
	}
	for _, message := range undeclared {
		resource, err := manifestResource(message)
		if err != nil {
			return provider.StackSpec{}, infraHeld{}, err
		}
		held = append(held, message)
		resources = append(resources, resource)
	}
	digest, err := r.digestInfraSpec()
	if err != nil {
		return provider.StackSpec{}, infraHeld{}, err
	}
	holds, err := encodeResources(held)
	if err != nil {
		return provider.StackSpec{}, infraHeld{}, err
	}
	recorded, err := r.isRecorded(ctx, r.spec.Infra)
	if err != nil {
		return provider.StackSpec{}, infraHeld{}, err
	}
	return provider.StackSpec{
		Recorded:  recorded,
		Ref:       r.ref(r.spec.Infra),
		Kind:      provider.StackInfra,
		Lease:     r.hold.lease(),
		Edge:      r.front,
		Tags:      infraTags(r.managedBy, r.spec),
		Resources: resources,
		Bindings:  r.publishedBindings(),
	}, infraHeld{holds: holds, digest: digest}, nil
}

type infraHeld struct {
	holds  []byte
	digest string
}

func (r *deployRun) provisionInfraStack(ctx context.Context, stack provider.StackSpec, held infraHeld, progress progress.Log) error {
	if err := r.recordInfraStack(ctx, held.holds); err != nil {
		return err
	}
	result, err := r.provider.Stacks().Provision(ctx, stack, progress)
	if err != nil {
		return err
	}
	for _, binding := range result.Bindings {
		if err := provider.VerifyProperties(binding); err != nil {
			return err
		}
	}
	if err := r.publish(ctx, result.Bindings); err != nil {
		return err
	}
	r.bindings = result.Bindings
	return stackrecords.Write(ctx, r.provider.KeyValues(), r.spec.Tier, r.spec.Slug, r.spec.Infra, stackrecords.Stack{
		Kind:       provider.StackInfra,
		Bindings:   result.Bindings,
		Resources:  held.holds,
		SpecDigest: held.digest,
		WrittenBy:  r.readiness.WrittenBy,
	})
}

func (r *deployRun) planBindings(ctx context.Context, stack provider.StackSpec) error {
	recorded, _, err := stackrecords.Read(ctx, r.provider.KeyValues(), r.spec.Tier, r.spec.Slug, r.spec.Infra)
	if err != nil {
		return err
	}
	var bindings []provider.Binding
	for _, resource := range stack.Resources {
		if resource.Binding != "" {
			continue
		}
		at := slices.IndexFunc(recorded.Bindings, func(binding provider.Binding) bool { return binding.Name == resource.Name })
		if at < 0 {
			bindings = append(bindings, provider.Binding{Type: resource.Type, Name: resource.Name})
			continue
		}
		bindings = append(bindings, recorded.Bindings[at])
	}
	r.bindings = bindings
	return nil
}

func (r *deployRun) provisionApp(ctx context.Context, slot int, entry provider.AppEntry) (appProvisioned, error) {
	var provisioned appProvisioned
	err := r.spanEvents.run(r.spans.Apps[entry.App], func(u *spanRun) error {
		return u.phase(func(progress progress.Log) error {
			release, err := r.appStackOf(ctx, entry)
			if err != nil {
				return err
			}
			provisioned, err = r.mode.app(ctx, r, slot, release, progress)
			return err
		})
	})
	return provisioned, err
}

type appStack struct {
	entry     provider.AppEntry
	spec      provider.StackSpec
	facts     AppServing
	images    provider.ImagePushes
	values    provider.AppValues
	createdAt int64
}

func (r *deployRun) appStackOf(ctx context.Context, entry provider.AppEntry) (appStack, error) {
	recorded, err := r.isRecorded(ctx, entry.Stack)
	if err != nil {
		return appStack{}, err
	}
	grants, err := r.grants(ctx, entry)
	if err != nil {
		return appStack{}, err
	}
	facts, err := r.appServing(entry)
	if err != nil {
		return appStack{}, err
	}
	values, err := r.appValues(ctx, entry, grants)
	if err != nil {
		return appStack{}, err
	}
	topics, err := r.declaredTopics()
	if err != nil {
		return appStack{}, err
	}
	images, err := r.imagePushes(ctx, entry)
	if err != nil {
		return appStack{}, err
	}
	discovered, err := r.readDiscoveredHealthPath(ctx, entry)
	if err != nil {
		return appStack{}, err
	}
	spec := provider.StackSpec{
		Ref:      r.ref(entry.Stack),
		Kind:     provider.StackApp,
		Lease:    r.hold.lease(),
		Recorded: recorded,
		Edge:     r.front,
		Tags:     appTags(r.managedBy, r.spec, entry),
		Images:   images,
		Bindings: r.publishedBindings(),
		App: &provider.AppSpec{
			App:                       entry.App,
			Framework:                 entry.Manifest.GetFramework().GetName(),
			RootFunction:              rootFunctionLogicalName(entry.Manifest, facts.RootFunction),
			BuildID:                   entry.Release.BuildID(),
			Compute:                   entry.Compute(),
			Router:                    r.appRouters[entry.App],
			Functions:                 r.functionSpecs(entry, facts.OriginTable()),
			Image:                     imageToRun(images, entry),
			HealthCheckPath:           entry.HealthCheckPath,
			DiscoveredHealthCheckPath: discovered,
			Arch:                      entry.Arch,
			Instances:                 entry.Instances,
			Workers:                   entry.Workers,
			DeploymentWorkers:         r.spec.Workers,
			Values:                    values,
			Grants:                    grants,
			Routing:                   facts.OriginDispatch,
			OcelRouteTable:            facts.OcelRouteTable,
			EdgeRouteTable:            facts.EdgeRouteTable,
			ISR:                       facts.ISR,
			StorageCoordinate:         appCoordinate(r.spec, entry.App, entry.Release.Token()),
			OutputRoot:                r.artifactRoot,
			Static:                    facts.Static,
			Guard:                     facts.Guard,
			Gate:                      r.appGate(),
			Proxied:                   anyProxied(proxied, grants),
			Topics:                    topics,
		},
	}
	return appStack{entry: entry, spec: spec, facts: facts, images: images, values: values, createdAt: time.Now().Unix()}, nil
}

func (r *deployRun) provisionAppStack(ctx context.Context, release appStack, progress progress.Log) (appProvisioned, error) {
	entry := release.entry
	provisioned := appProvisioned{provisioning: true}
	if err := r.recordAppStack(ctx, entry, release.images, provider.StackResult{}); err != nil {
		return provisioned, err
	}
	result, err := r.provider.Stacks().Provision(ctx, release.spec, progress)
	if err != nil {
		return provisioned, err
	}
	if err := r.recordAppStack(ctx, entry, release.images, result); err != nil {
		return provisioned, err
	}
	note, err := r.gateTurnedOn(ctx, result.Functions)
	if err != nil {
		return provisioned, err
	}
	if note != "" {
		progress.Warn(note)
		provisioned.notes = append(provisioned.notes, note)
	}
	address, deployment := findOwnAddresses(entry, release.facts, result)
	provisioned.address = address
	provisioned.deployment = r.deploymentHost(entry.App, deployment)
	return provisioned, r.recordStagedRelease(ctx, release, result)
}

func (r *deployRun) recordAppStack(ctx context.Context, entry provider.AppEntry, images provider.ImagePushes, result provider.StackResult) error {
	return stackrecords.Write(ctx, r.provider.KeyValues(), r.spec.Tier, r.spec.Slug, entry.Stack, stackrecords.Stack{
		Kind:         provider.StackApp,
		App:          entry.App,
		ReleaseToken: entry.Release.Token().String(),
		Release:      entry.Release.String(),
		Functions:    result.Functions,
		Containers:   result.Containers,
		Image:        images.ImageRef(entry.App),
		WrittenBy:    r.readiness.WrittenBy,
	})
}

func (r *deployRun) isRecorded(ctx context.Context, stack naming.StackName) (bool, error) {
	_, recorded, err := stackrecords.Read(ctx, r.provider.KeyValues(), r.spec.Tier, r.spec.Slug, stack)
	return recorded, err
}

func (r *deployRun) appServing(entry provider.AppEntry) (AppServing, error) {
	facts, err := AppServingFor(AppServingInput{
		Root:              r.artifactRoot,
		Project:           naming.Sanitize(r.spec.Slug),
		App:               entry.App,
		Framework:         entry.Manifest.GetFramework().GetName(),
		Compute:           entry.Compute(),
		Stack:             entry.Stack,
		Coordinate:        appCoordinate(r.spec, entry.App, entry.Release.Token()),
		EdgeRunsCode:      r.front.Facts().RunsCode,
		EdgeSignsForwards: r.readPairedRouter(entry.App).Facts().SignsOriginForwards,
	})
	if err != nil {
		return AppServing{}, err
	}
	if stack := r.edgeStack(); facts.ISR != nil && stack != nil {
		facts.ISR.PurgeURL = stack.State().PurgeURL
	}
	return facts, nil
}

func (r *deployRun) boundNamesUsedBy(app string) (map[string]bool, error) {
	resources, err := manifestResources(r.manifest)
	if err != nil {
		return nil, err
	}
	return boundNamesUsedBy(r.manifest, resources, app), nil
}

func boundNamesUsedBy(manifest *contractv1.Manifest, resources []provider.Resource, app string) map[string]bool {
	names := map[string]bool{}
	for _, resource := range resourcesUsedBy(manifest, resources, app) {
		names[boundName(resource)] = true
	}
	return names
}

func resourcesUsedBy(manifest *contractv1.Manifest, resources []provider.Resource, app string) []provider.Resource {
	edges := map[string]bool{}
	for _, usage := range manifest.GetUsages() {
		if usage.GetApp() == app {
			edges[usage.GetResource()] = true
		}
	}
	var used []provider.Resource
	for _, resource := range resources {
		if edges[resource.Name] {
			used = append(used, resource)
		}
	}
	return used
}

func (r *deployRun) grants(ctx context.Context, entry provider.AppEntry) ([]provider.Binding, error) {
	resources, err := manifestResources(r.manifest)
	if err != nil {
		return nil, err
	}
	used := resourcesUsedBy(r.manifest, resources, entry.App)
	unbound := map[string]bool{}
	for _, resource := range used {
		if resource.Binding == "" {
			unbound[resource.Name] = true
		}
	}
	var grants []provider.Binding
	for _, binding := range r.bindings {
		if unbound[binding.Name] {
			grants = append(grants, binding)
		}
	}
	provisioned := slices.Clone(grants)
	consumed, err := r.publishedBindings().Published(ctx)
	if err != nil {
		return nil, err
	}
	for _, resource := range used {
		name := boundName(resource)
		if resource.Binding == "" && slices.ContainsFunc(provisioned, func(grant provider.Binding) bool { return grant.Name == name }) {
			continue
		}
		at := slices.IndexFunc(consumed, func(binding provider.Binding) bool { return binding.Name == name })
		if at < 0 {
			continue
		}
		binding := consumed[at]
		if resource.Binding != "" {
			binding.Resource = resource.Declared
		}
		grants = append(grants, binding)
	}
	slices.SortFunc(grants, func(a, b provider.Binding) int {
		return cmp.Or(strings.Compare(a.Name, b.Name), strings.Compare(a.Resource, b.Resource))
	})
	return grants, nil
}

func (r *deployRun) appValues(ctx context.Context, entry provider.AppEntry, grants []provider.Binding) (provider.AppValues, error) {
	values, err := r.manifestValues(ctx, entry, grants)
	if err != nil {
		return provider.AppValues{}, err
	}
	values.ContainerEnv = r.containerEnv(entry, values)
	return values, nil
}

func (r *deployRun) manifestValues(ctx context.Context, entry provider.AppEntry, grants []provider.Binding) (provider.AppValues, error) {
	values := provider.AppValues{
		Plain:     map[string]string{},
		Sensitive: map[string]string{},
		Owners:    map[string]string{},
		Bindings:  grants,
		Folder:    entry.Manifest.GetFolder(),
		Phase:     r.spec.Phase,
	}
	for _, variable := range entry.Manifest.GetVariables() {
		value := variable.GetValue()
		if variable.GetBinding() != "" && variable.GetClass() != resourcesv1.VariableClass_VARIABLE_CLASS_SECRET {
			text, err := r.boundText(ctx, boundVariable{app: entry.App, variable: variable})
			if err != nil {
				return provider.AppValues{}, err
			}
			value = text
		}
		switch variable.GetClass() {
		case resourcesv1.VariableClass_VARIABLE_CLASS_SECRET:
			values.Secrets = append(values.Secrets, provider.SecretRef{
				Key:      variable.GetKey(),
				Folder:   variable.GetFolder(),
				Binding:  variable.GetBinding(),
				Property: variable.GetProperty(),
			})
		case resourcesv1.VariableClass_VARIABLE_CLASS_SENSITIVE:
			values.Sensitive[variable.GetKey()] = value
		case resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN:
			values.Plain[variable.GetKey()] = value
		default:
			return provider.AppValues{}, refusal.Refuse(refusal.CodeInvalid,
				"%s declares %s with class %s, which this deploy cannot deliver to a function; declare it as `plain`, `sensitive` or `secret`",
				entry.App, variable.GetKey(), variable.GetClass())
		}
		values.Owners[variable.GetKey()] = entry.App
	}
	return values, nil
}

func (r *deployRun) functionSpecs(entry provider.AppEntry, routing *provider.RoutingSpec) []provider.FunctionSpec {
	var specs []provider.FunctionSpec
	for _, fn := range entry.Manifest.GetServerless().GetFunctions() {
		spec := functionSpecOf(fn)
		spec.ArtifactDir = artifactDir(r.artifactRoot, fn)
		spec.Overlay = overlayFor(fn, routing)
		specs = append(specs, spec)
	}
	return specs
}

func (r *deployRun) deploymentHost(app, address string) edge.PreviewHost {
	if address == "" || r.spec.Tier != environment.TierPreview || r.previewOn != "" || !r.readPairedRouter(app).Facts().AddressesItself {
		return edge.PreviewHost{}
	}
	parsed, err := url.Parse(address)
	if err != nil || parsed.Host == "" {
		return edge.PreviewHost{}
	}
	return edge.PreviewHost{Hostname: parsed.Host, App: normalizeAppName(app)}
}

func (r *deployRun) readDiscoveredHealthPath(ctx context.Context, entry provider.AppEntry) (string, error) {
	if entry.Compute() != provider.ComputeContainer || entry.HealthCheckPath != "" {
		return "", nil
	}
	promotion, active, err := r.ledger.ReadActive(ctx, r.spec.Pointer)
	if err != nil || !active {
		return "", err
	}
	release, promoted := promotion.Releases[entry.App]
	if !promoted {
		return "", nil
	}
	record, staged, err := r.ledger.Record(ctx, entry.App, release)
	if err != nil || !staged || !record.HealthPathDiscovered {
		return "", err
	}
	return record.HealthPath, nil
}

func imageToRun(images provider.ImagePushes, entry provider.AppEntry) string {
	if pushed := images.ImageRef(entry.App); pushed != "" {
		return pushed
	}
	return entry.Image
}

func frameworkOf(fn *contractv1.ManifestFunction) buildoutput.Framework {
	return buildoutput.Framework{Name: fn.GetFramework().GetName(), Arch: fn.GetFramework().GetArch()}
}
