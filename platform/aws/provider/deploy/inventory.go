package deploy

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/ocelhq/ocel/platform/aws/provider/tier/sharedparts"

	"github.com/ocelhq/ocel/platform/aws/provider/cost"

	ocelpulumi "github.com/ocelhq/ocel/pkg/provider/pulumi"
	"github.com/ocelhq/ocel/platform/aws/provider/compute/worker"

	"github.com/ocelhq/ocel/platform/aws/provider/compute/container"
	"github.com/ocelhq/ocel/platform/aws/provider/compute/function"
	awsresources "github.com/ocelhq/ocel/platform/aws/provider/resources"

	"github.com/ocelhq/ocel/pkg/arch"
	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/pricing"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/resources"
	"github.com/ocelhq/ocel/pkg/provider/transform"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/aws/provider/component"
	"github.com/ocelhq/ocel/platform/aws/provider/edges"
	"github.com/ocelhq/ocel/platform/aws/provider/edges/alb"
	awsports "github.com/ocelhq/ocel/platform/aws/provider/ports"
	"github.com/ocelhq/ocel/platform/aws/provider/resources/task"
	"github.com/ocelhq/ocel/platform/aws/provider/resources/topic"
	"github.com/ocelhq/ocel/platform/aws/provider/tier/functionnetwork"
)

type InventoryScopes struct {
	Environment string
	Shared      string
}

type inventoryPatches struct {
	patches map[ocelpulumi.ResourceRef]map[string]any
	unknown map[ocelpulumi.ResourceRef][]string
}

func Inventory(ctx context.Context, pass transform.Pass, namespace, region string, req provider.InventoryRequest, tree *pricing.Tree, scopes InventoryScopes) error {
	project := naming.Sanitize(req.Deploy.Slug)
	registry := awsresources.Registry(awsresources.Clients{})
	at := component.Stack{Ref: provider.StackRef{Tier: req.Deploy.Tier, Project: req.Deploy.Slug}, Namespace: namespace, Project: project, Env: req.Deploy.Env, Region: region, Resources: req.Resources}
	patched, err := inventoryTransforms(ctx, pass, registry, at, req)
	if err != nil {
		return err
	}
	inventory := costInventory{tree: tree, region: region, patched: patched}
	for _, registered := range registry.Components() {
		if registered.Inventory == nil {
			continue
		}
		if err := registered.Inventory(inventory, scopes.Environment, at, registered.Declared(req.Resources)); err != nil {
			return err
		}
	}
	topics, err := topic.Of(topic.NamesAt(at), req.Resources)
	if err != nil {
		return err
	}
	if err := inventory.workers(scopes.Environment, req, topics); err != nil {
		return err
	}
	hasContainers := false
	for _, app := range req.Deploy.Apps {
		scope := tree.Scope(scopes.Environment, pricing.ScopeApp, app.App)
		if app.Compute() == provider.ComputeContainer {
			inventory.container(scope, app)
			hasContainers = true
			continue
		}
		if err := inventory.functions(scope, at, app, req.Functions[app.App]); err != nil {
			return err
		}
	}
	if hasContainers {
		public := routesContainersPublicly(req.Edge)
		tree.AddResources(scopes.Shared, awsports.Vendor, region, sharedparts.ContainersInventory(req.Deploy.Tier, sharedparts.RaisedFronts{Private: !public, Public: public}))
	}
	if reachesVPCFromFunctions(req) {
		tree.AddResources(scopes.Shared, awsports.Vendor, region, sharedparts.FunctionNetworkInventory(req.Deploy.Tier))
	}
	if hasContainers || reachesVPCFromFunctions(req) || len(registry.PlacesInVPC(req.Resources)) > 0 {
		tree.AddResources(scopes.Shared, awsports.Vendor, region, sharedparts.NetworkInventory(req.Deploy.Tier))
	}
	return nil
}

type costInventory struct {
	tree    *pricing.Tree
	region  string
	patched inventoryPatches
}

func (s costInventory) Add(scope, typ, name string, properties map[string]any, ref ocelpulumi.ResourceRef) {
	unknown := s.patched.unknown[ref]
	for key, value := range s.patched.patches[ref] {
		field := snake(key)
		if slices.Contains(unknown, field) {
			delete(properties, field)
			continue
		}
		properties[field] = snakeValue(value)
	}
	s.tree.Add(scope, awsports.Vendor, typ, name, s.region, properties, unknown...)
}

func (s costInventory) AddPlain(scope, typ, name string, properties map[string]any) {
	s.tree.Add(scope, awsports.Vendor, typ, name, s.region, properties)
}

func (s costInventory) functions(scope string, at component.Stack, app provider.AppEntry, specs []provider.FunctionSpec) error {
	framework := app.Manifest.GetFramework().GetName()
	if len(specs) == 0 {
		specs = []provider.FunctionSpec{{Name: app.App}}
	}
	for _, spec := range specs {
		if spec.Framework.Name == "" {
			spec.Framework.Name = framework
		}
		if spec.Framework.Arch == "" {
			spec.Framework.Arch = arch.Architecture(app.Manifest.GetFramework().GetArch())
		}
		args, err := function.NewArgs(framework, spec)
		if err != nil {
			return err
		}
		names, err := function.NamesFor(at.Namespace, at.Project, app.Stack, spec.Name)
		if err != nil {
			return err
		}
		s.Add(scope, cost.TerraformLambdaFunction, spec.Name, map[string]any{
			"runtime":           args.Runtime,
			"memory_size":       args.MemorySizeMB,
			"timeout":           args.TimeoutSeconds,
			"architectures":     []any{args.Arch},
			"ephemeral_storage": map[string]any{"size": awsports.LambdaDefaultEphemeralMB},
		}, names.Lambda())
		s.Add(scope, cost.TerraformLogGroup, spec.Name, map[string]any{"retention_in_days": component.LambdaLogRetentionDays}, names.LogGroup())
		s.Add(scope, cost.TerraformLambdaFunctionURL, spec.Name, map[string]any{"invoke_mode": args.InvokeMode}, names.URL())
	}
	return nil
}

func (s costInventory) workers(environment string, req provider.InventoryRequest, topics topic.Topics) error {
	for _, app := range req.Deploy.Apps {
		if len(app.Workers) == 0 {
			continue
		}
		if app.Compute() == provider.ComputeContainer {
			if err := s.containerWorkers(environment, app, topics); err != nil {
				return err
			}
			continue
		}
		if err := s.appWorkers(environment, req, app, topics); err != nil {
			return err
		}
	}
	return nil
}

func (s costInventory) appWorkers(environment string, req provider.InventoryRequest, app provider.AppEntry, topics topic.Topics) error {
	scope := s.tree.Scope(environment, pricing.ScopeApp, app.App)
	args, err := workerFunctionArgs(app, req.Functions[app.App])
	if err != nil {
		return err
	}
	for _, worker := range app.Workers {
		s.AddPlain(scope, cost.TerraformLambdaFunction, worker.Name, map[string]any{
			"runtime":           args.Runtime,
			"memory_size":       args.MemorySizeMB,
			"timeout":           int(topic.WorkerTimeout(topics, worker.Name).Seconds()),
			"architectures":     []any{args.Arch},
			"ephemeral_storage": map[string]any{"size": awsports.LambdaDefaultEphemeralMB},
		})
		s.AddPlain(scope, cost.TerraformLogGroup, worker.Name, map[string]any{"retention_in_days": component.LambdaLogRetentionDays})
		for _, queue := range topics.Served(worker.Name) {
			s.AddPlain(scope, cost.TerraformEventSourceMapping, queue.Topic+"/"+queue.Consumer.Name, map[string]any{"batch_size": batchSize(queue.Consumer)})
		}
		if err := s.cronSchedules(scope, topics, worker.Name); err != nil {
			return err
		}
	}
	return nil
}

func (s costInventory) cronSchedules(scope string, topics topic.Topics, worker string) error {
	for _, cronTask := range topics.CronTasks(worker) {
		expressions, err := task.SchedulerExpressions(cronTask.Spec.Cron)
		if err != nil {
			return fmt.Errorf("task %s: %w", cronTask.Resource.Declared, err)
		}
		for _, expression := range expressions {
			s.AddPlain(scope, cost.TerraformSchedulerSchedule, cronTask.Resource.Declared, map[string]any{"schedule_expression": expression})
		}
	}
	return nil
}

func (s costInventory) containerWorkers(environment string, app provider.AppEntry, topics topic.Topics) error {
	scope := s.tree.Scope(environment, pricing.ScopeApp, app.App)
	for _, hosted := range app.Workers {
		name := resources.WorkerName(hosted.Name)
		platform := map[string]any{"cpu_architecture": container.CPUArchitecture(app.Arch), "operating_system_family": "LINUX"}
		s.AddPlain(scope, cost.TerraformECSTaskDefinition, name, map[string]any{
			"cpu": container.CPU, "memory": container.Memory, "requires_compatibilities": []any{"FARGATE"}, "runtime_platform": platform,
		})
		s.AddPlain(scope, cost.TerraformECSService, name, map[string]any{
			"desired_count": worker.Floor(app.App, app.Instances, app.Workers), "launch_type": "FARGATE", "cpu": container.CPU, "memory": container.Memory, "runtime_platform": platform,
		})
		var queueNames []string
		for _, queue := range topics.Served(hosted.Name) {
			queueNames = append(queueNames, queue.Name)
		}
		for at, group := range slices.Collect(slices.Chunk(queueNames, worker.BacklogAlarmQueues)) {
			s.AddPlain(scope, cost.TerraformMetricAlarm, name+"/"+strconv.Itoa(at), map[string]any{"metrics": len(group)})
		}
		if err := s.cronSchedules(scope, topics, hosted.Name); err != nil {
			return err
		}
	}
	return nil
}

func batchSize(consumer provider.ConsumerSpec) int {
	if consumer.Batch == nil {
		return 1
	}
	return max(consumer.Batch.Size, 1)
}

func workerFunctionArgs(app provider.AppEntry, specs []provider.FunctionSpec) (function.Args, error) {
	framework := app.Manifest.GetFramework().GetName()
	spec := provider.FunctionSpec{Name: app.App}
	if at := slices.IndexFunc(specs, func(fn provider.FunctionSpec) bool { return fn.HasWorkerEntry }); at >= 0 {
		spec = specs[at]
	}
	if spec.Framework.Name == "" {
		spec.Framework.Name = framework
	}
	if spec.Framework.Arch == "" {
		spec.Framework.Arch = arch.Architecture(app.Manifest.GetFramework().GetArch())
	}
	return function.NewArgs(framework, spec)
}

func (s costInventory) container(scope string, app provider.AppEntry) {
	if !provider.IsWorkerApp(app.App, app.Workers) {
		s.AddPlain(scope, cost.TerraformECSTaskDefinition, app.App, map[string]any{
			"cpu":                      container.CPU,
			"memory":                   container.Memory,
			"requires_compatibilities": []any{"FARGATE"},
			"runtime_platform":         map[string]any{"cpu_architecture": container.CPUArchitecture(app.Arch), "operating_system_family": "LINUX"},
		})
		s.AddPlain(scope, cost.TerraformECSService, app.App, map[string]any{
			"desired_count":    app.Instances.Min,
			"launch_type":      "FARGATE",
			"cpu":              container.CPU,
			"memory":           container.Memory,
			"runtime_platform": map[string]any{"cpu_architecture": container.CPUArchitecture(app.Arch), "operating_system_family": "LINUX"},
		})
	}
	s.AddPlain(scope, cost.TerraformECRRepository, app.App, map[string]any{"image_tag_mutability": "IMMUTABLE"})
}

func routesContainersPublicly(front edge.Kind) bool {
	paired, _ := provider.Facts{Pairings: edges.Pairings()}.PairedRouter(front, provider.ComputeContainer)
	return paired == alb.Kind
}

func reachesVPCFromFunctions(req provider.InventoryRequest) bool {
	serverless := map[string]bool{}
	for _, app := range req.Deploy.Apps {
		serverless[app.App] = app.Compute() != provider.ComputeContainer
	}
	for _, usage := range req.Usages {
		if !serverless[usage.App] {
			continue
		}
		provisioned := slices.ContainsFunc(usage.Resources, func(resource provider.Resource) bool {
			return functionnetwork.IsNeededFor(resource.Type, resource.Binding)
		})
		if provisioned {
			return true
		}
	}
	return false
}

func inventoryTransforms(ctx context.Context, pass transform.Pass, registry component.Registry, at component.Stack, req provider.InventoryRequest) (inventoryPatches, error) {
	collected := inventoryPatches{patches: map[ocelpulumi.ResourceRef]map[string]any{}, unknown: map[ocelpulumi.ResourceRef][]string{}}
	if pass == nil {
		return collected, nil
	}
	request := transform.Request{Provider: transformProvider, EnvTier: string(req.Deploy.Tier), Env: req.Deploy.Env}
	var candidates []transformCandidate
	for _, resource := range req.Resources {
		if resource.Binding != "" {
			continue
		}
		shown, candidate, patchable, err := newInfraTransformCandidate(registry, at, resource)
		if err != nil {
			return collected, err
		}
		if patchable {
			request.Resources = append(request.Resources, shown)
			candidates = append(candidates, candidate)
		}
	}
	for _, app := range req.Deploy.Apps {
		if app.Compute() == provider.ComputeContainer {
			continue
		}
		specs := req.Functions[app.App]
		if len(specs) == 0 {
			specs = []provider.FunctionSpec{{Name: app.App}}
		}
		for _, spec := range specs {
			names, err := function.NamesFor(at.Namespace, at.Project, app.Stack, spec.Name)
			if err != nil {
				return collected, err
			}
			request.Resources = append(request.Resources, transform.Resource{Type: transformTypeFunction, Name: spec.Name, App: app.App})
			candidates = append(candidates, transformCandidate{key: resourceKey{Type: transformTypeFunction, Name: spec.Name}, names: names.Surfaces()})
		}
	}
	if len(candidates) == 0 {
		return collected, nil
	}
	results, err := pass.Evaluate(ctx, request)
	if err != nil {
		return collected, err
	}
	if results == nil {
		return collected, nil
	}
	if len(results) != len(candidates) {
		return collected, refusal.Refuse(refusal.CodeInvalid, "transforms returned %d results for %d resources", len(results), len(candidates))
	}
	unresolved := map[transform.OutputSite]bool{}
	if err := transform.WalkOutputs(request, results, func(_ transform.OutputRef, at transform.OutputSite, authored any) (any, error) {
		unresolved[at] = true
		return authored, nil
	}); err != nil {
		return collected, err
	}
	for i, candidate := range candidates {
		for _, key := range slices.Sorted(maps.Keys(results[i].Patches)) {
			refs, constructed := candidate.names[key]
			if !constructed {
				return collected, refusal.Refuse(refusal.CodeInvalid,
					"a transform patches %s's %s, and this deploy constructs no such resource for it", candidate.key.Name, key)
			}
			for _, ref := range refs {
				for field, value := range results[i].Patches[key] {
					if unresolved[transform.OutputSite{Resource: candidate.key.Name, Surface: key, Field: field}] {
						collected.unknown[ref] = append(collected.unknown[ref], snake(field))
						continue
					}
					if collected.patches[ref] == nil {
						collected.patches[ref] = map[string]any{}
					}
					collected.patches[ref][field] = value
				}
			}
		}
	}
	return collected, nil
}

func snakeValue(value any) any {
	switch v := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(v))
		for key, inner := range v {
			out[snake(key)] = snakeValue(inner)
		}
		return out
	case []any:
		out := make([]any, 0, len(v))
		for _, inner := range v {
			out = append(out, snakeValue(inner))
		}
		return out
	}
	return value
}

func snake(camel string) string {
	var b strings.Builder
	for i, r := range camel {
		if unicode.IsUpper(r) {
			if i > 0 {
				b.WriteByte('_')
			}
			b.WriteRune(unicode.ToLower(r))
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}
