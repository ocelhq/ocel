package deploy

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"unicode"

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
	"github.com/ocelhq/ocel/platform/aws/provider/tier/containers"
	"github.com/ocelhq/ocel/platform/aws/provider/tier/functionnetwork"
)

type ShapeScopes struct {
	Environment string
	Shared      string
}

type shapedPatches struct {
	patches map[component.ResourceRef]map[string]any
	unknown map[component.ResourceRef][]string
}

func Shape(ctx context.Context, pass transform.Pass, region string, req provider.ShapeRequest, tree *pricing.Tree, scopes ShapeScopes) error {
	project := naming.Sanitize(req.Deploy.Slug)
	registry := awsresources.Registry(awsresources.Clients{})
	patched, err := shapeTransforms(ctx, pass, registry, project, req)
	if err != nil {
		return err
	}
	shape := costShape{tree: tree, region: region, patched: patched}
	at := component.Stack{Ref: provider.StackRef{Tier: req.Deploy.Tier, Project: req.Deploy.Slug}, Project: project, Env: req.Deploy.Env, Region: region, Resources: req.Resources}
	for _, shaped := range registry.Components() {
		if shaped.Shape == nil {
			continue
		}
		if err := shaped.Shape(shape, scopes.Environment, at, shaped.Declared(req.Resources)); err != nil {
			return err
		}
	}
	topics := topic.Of(project, req.Deploy.Env, req.Resources)
	if err := shape.workers(scopes.Environment, req, topics); err != nil {
		return err
	}
	hasContainers := false
	for _, app := range req.Deploy.Apps {
		scope := tree.Scope(scopes.Environment, pricing.ScopeApp, app.App)
		if app.Compute() == provider.ComputeContainer {
			shape.container(scope, app)
			hasContainers = true
			continue
		}
		if err := shape.functions(scope, project, app, req.Functions[app.App]); err != nil {
			return err
		}
	}
	if hasContainers {
		public := routesContainersPublicly(req.Edge)
		tree.AddShaped(scopes.Shared, awsports.Vendor, region, containers.Shape(req.Deploy.Tier, containers.Raised{Private: !public, Public: public}))
	}
	if reachesVPCFromFunctions(req) {
		tree.AddShaped(scopes.Shared, awsports.Vendor, region, functionnetwork.Shape(req.Deploy.Tier))
	}
	return nil
}

type costShape struct {
	tree    *pricing.Tree
	region  string
	patched shapedPatches
}

func (s costShape) Add(scope, typ, name string, properties map[string]any, ref component.ResourceRef) {
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

func (s costShape) AddPlain(scope, typ, name string, properties map[string]any) {
	s.tree.Add(scope, awsports.Vendor, typ, name, s.region, properties)
}

func (s costShape) functions(scope, project string, app provider.AppEntry, specs []provider.FunctionSpec) error {
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
		names := function.ResourceNames(project, app.Stack, spec.Name)
		s.Add(scope, component.TerraformLambdaFunction, spec.Name, map[string]any{
			"runtime":           args.Runtime,
			"memory_size":       args.MemorySizeMB,
			"timeout":           args.TimeoutSeconds,
			"architectures":     []any{args.Arch},
			"ephemeral_storage": map[string]any{"size": component.LambdaDefaultEphemeralMB},
		}, names["lambda"])
		s.Add(scope, component.TerraformLogGroup, spec.Name, map[string]any{"retention_in_days": component.LambdaLogRetentionDays}, names["logGroup"])
		s.Add(scope, component.TerraformLambdaFunctionURL, spec.Name, map[string]any{"invoke_mode": args.InvokeMode}, names["url"])
	}
	return nil
}

func (s costShape) workers(environment string, req provider.ShapeRequest, topics topic.Topics) error {
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

func (s costShape) appWorkers(environment string, req provider.ShapeRequest, app provider.AppEntry, topics topic.Topics) error {
	scope := s.tree.Scope(environment, pricing.ScopeApp, app.App)
	args, err := workerFunctionArgs(app, req.Functions[app.App])
	if err != nil {
		return err
	}
	for _, worker := range app.Workers {
		s.AddPlain(scope, component.TerraformLambdaFunction, worker.Name, map[string]any{
			"runtime":           args.Runtime,
			"memory_size":       args.MemorySizeMB,
			"timeout":           int(topic.WorkerTimeout(topics, worker.Name).Seconds()),
			"architectures":     []any{args.Arch},
			"ephemeral_storage": map[string]any{"size": component.LambdaDefaultEphemeralMB},
		})
		s.AddPlain(scope, component.TerraformLogGroup, worker.Name, map[string]any{"retention_in_days": component.LambdaLogRetentionDays})
		for _, queue := range topics.Served(worker.Name) {
			s.AddPlain(scope, component.TerraformEventSourceMapping, queue.Topic+"/"+queue.Consumer.Name, map[string]any{"batch_size": batchSize(queue.Consumer)})
		}
		if err := s.cronSchedules(scope, topics, worker.Name); err != nil {
			return err
		}
	}
	return nil
}

func (s costShape) cronSchedules(scope string, topics topic.Topics, worker string) error {
	for _, cronTask := range topics.CronTasks(worker) {
		expressions, err := task.SchedulerExpressions(cronTask.Spec.Cron)
		if err != nil {
			return fmt.Errorf("task %s: %w", cronTask.Resource.Declared, err)
		}
		for _, expression := range expressions {
			s.AddPlain(scope, component.TerraformSchedulerSchedule, cronTask.Resource.Declared, map[string]any{"schedule_expression": expression})
		}
	}
	return nil
}

func (s costShape) containerWorkers(environment string, app provider.AppEntry, topics topic.Topics) error {
	scope := s.tree.Scope(environment, pricing.ScopeApp, app.App)
	for _, hosted := range app.Workers {
		name := resources.WorkerName(hosted.Name)
		platform := map[string]any{"cpu_architecture": container.CPUArchitecture(app.Arch), "operating_system_family": "LINUX"}
		s.AddPlain(scope, component.TerraformECSTaskDefinition, name, map[string]any{
			"cpu": container.CPU, "memory": container.Memory, "requires_compatibilities": []any{"FARGATE"}, "runtime_platform": platform,
		})
		s.AddPlain(scope, component.TerraformECSService, name, map[string]any{
			"desired_count": worker.Floor(app.App, app.Instances, app.Workers), "launch_type": "FARGATE", "cpu": container.CPU, "memory": container.Memory, "runtime_platform": platform,
		})
		var queueNames []string
		for _, queue := range topics.Served(hosted.Name) {
			queueNames = append(queueNames, queue.Name)
		}
		for at, group := range slices.Collect(slices.Chunk(queueNames, worker.BacklogAlarmQueues)) {
			s.AddPlain(scope, component.TerraformMetricAlarm, name+"/"+strconv.Itoa(at), map[string]any{"metrics": len(group)})
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

func (s costShape) container(scope string, app provider.AppEntry) {
	if !provider.IsWorkerApp(app.App, app.Workers) {
		s.AddPlain(scope, component.TerraformECSTaskDefinition, app.App, map[string]any{
			"cpu":                      container.CPU,
			"memory":                   container.Memory,
			"requires_compatibilities": []any{"FARGATE"},
			"runtime_platform":         map[string]any{"cpu_architecture": container.CPUArchitecture(app.Arch), "operating_system_family": "LINUX"},
		})
		s.AddPlain(scope, component.TerraformECSService, app.App, map[string]any{
			"desired_count":    app.Instances.Min,
			"launch_type":      "FARGATE",
			"cpu":              container.CPU,
			"memory":           container.Memory,
			"runtime_platform": map[string]any{"cpu_architecture": container.CPUArchitecture(app.Arch), "operating_system_family": "LINUX"},
		})
	}
	s.AddPlain(scope, component.TerraformECRRepository, app.App, map[string]any{"image_tag_mutability": "IMMUTABLE"})
}

func routesContainersPublicly(front edge.Kind) bool {
	paired, _ := provider.Facts{Pairings: edges.Pairings()}.PairedRouter(front, provider.ComputeContainer)
	return paired == alb.Kind
}

func reachesVPCFromFunctions(req provider.ShapeRequest) bool {
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

func shapeTransforms(ctx context.Context, pass transform.Pass, registry component.Registry, project string, req provider.ShapeRequest) (shapedPatches, error) {
	collected := shapedPatches{patches: map[component.ResourceRef]map[string]any{}, unknown: map[component.ResourceRef][]string{}}
	if pass == nil {
		return collected, nil
	}
	request := transform.Request{Provider: transformProvider, EnvTier: string(req.Deploy.Tier), Env: req.Deploy.Env}
	var candidates []transformCandidate
	for _, resource := range req.Resources {
		if resource.Binding != "" {
			continue
		}
		if shown, candidate, patchable := newInfraTransformCandidate(registry, project, req.Deploy.Env, resource); patchable {
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
			request.Resources = append(request.Resources, transform.Resource{Type: transformTypeFunction, Name: spec.Name, App: app.App})
			candidates = append(candidates, transformCandidate{key: resourceKey{Type: transformTypeFunction, Name: spec.Name}, names: function.ResourceNames(project, app.Stack, spec.Name)})
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
	unresolved := map[outputSite]bool{}
	if err := walkOutputs(candidates, results, func(_ outputRef, at outputSite, authored any) (any, error) {
		unresolved[at] = true
		return authored, nil
	}); err != nil {
		return collected, err
	}
	for i, candidate := range candidates {
		for _, key := range slices.Sorted(maps.Keys(results[i].Patches)) {
			ref, constructed := candidate.names[key]
			if !constructed {
				return collected, refusal.Refuse(refusal.CodeInvalid,
					"a transform patches %s's %s, and this deploy constructs no such resource for it", candidate.key.Name, key)
			}
			for field, value := range results[i].Patches[key] {
				if unresolved[outputSite{Resource: candidate.key.Name, Surface: key, Field: field}] {
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
