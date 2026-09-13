package deploy

import (
	"context"
	"maps"
	"slices"
	"strings"
	"unicode"

	"github.com/ocelhq/ocel/pkg/costkit"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/platform/aws/provider/transform"
)

const (
	vendor = "aws"

	tfLambdaFunction     = "aws_lambda_function"
	tfLambdaFunctionURL  = "aws_lambda_function_url"
	tfLogGroup           = "aws_cloudwatch_log_group"
	tfS3Bucket           = "aws_s3_bucket"
	tfRDSCluster         = "aws_rds_cluster"
	tfRDSClusterInstance = "aws_rds_cluster_instance"
	tfSecret             = "aws_secretsmanager_secret"
	tfECSCluster         = "aws_ecs_cluster"
	tfECSTaskDefinition  = "aws_ecs_task_definition"
	tfECSService         = "aws_ecs_service"
	tfLoadBalancer       = "aws_lb"
	tfECRRepository      = "aws_ecr_repository"

	lambdaDefaultMemoryMB    = 128
	lambdaDefaultEphemeralMB = 512
	fargateDesiredCount      = 1
)

type ShapeScopes struct {
	Environment string
	Shared      string
}

type shapedPatches struct {
	patches map[resourceRef]map[string]any
	unknown map[resourceRef][]string
}

func Shape(ctx context.Context, evaluator transform.Evaluator, region string, req providerkit.ShapeRequest, tree *costkit.Tree, scopes ShapeScopes) error {
	project := naming.Sanitize(req.Plan.Slug)
	patched, err := shapeTransforms(ctx, evaluator, project, req)
	if err != nil {
		return err
	}
	shape := shaper{tree: tree, region: region, patched: patched}
	for _, resource := range req.Resources {
		if resource.Binding != "" {
			continue
		}
		switch resource.Type {
		case providerkit.BindingPostgres:
			shape.postgres(scopes.Environment, project, req.Plan.Env, resource)
		case providerkit.BindingBucket:
			shape.bucket(scopes.Environment, project, req.Plan.Env, resource)
		}
	}
	substrate := false
	for _, app := range req.Plan.Apps {
		scope := tree.Scope(scopes.Environment, "app", app.App)
		if app.Compute() == providerkit.ComputeContainer {
			shape.container(scope, app)
			substrate = true
			continue
		}
		if err := shape.functions(scope, project, app, req.Functions[app.App]); err != nil {
			return err
		}
	}
	if substrate {
		shape.substrate(scopes.Shared, req.Plan.Class)
	}
	return nil
}

type shaper struct {
	tree    *costkit.Tree
	region  string
	patched shapedPatches
}

func (s shaper) add(scope, typ, name string, properties map[string]any, ref resourceRef) {
	unknown := s.patched.unknown[ref]
	for key, value := range s.patched.patches[ref] {
		field := snake(key)
		if slices.Contains(unknown, field) {
			delete(properties, field)
			continue
		}
		properties[field] = snakeValue(value)
	}
	s.tree.Add(scope, vendor, typ, name, s.region, properties, unknown...)
}

func (s shaper) plain(scope, typ, name string, properties map[string]any) {
	s.tree.Add(scope, vendor, typ, name, s.region, properties)
}

func (s shaper) functions(scope, project string, app providerkit.AppEntry, specs []providerkit.FunctionSpec) error {
	runtime := app.Manifest.GetRuntime().GetName()
	if len(specs) == 0 {
		specs = []providerkit.FunctionSpec{{Name: app.App, URL: true}}
	}
	for _, spec := range specs {
		if spec.Runtime.Name == "" {
			spec.Runtime.Name = runtime
		}
		if spec.Runtime.Arch == "" {
			spec.Runtime.Arch = providerkit.Architecture(app.Manifest.GetRuntime().GetArch())
		}
		args, err := translateFunctionSpec(runtime, spec)
		if err != nil {
			return err
		}
		names := functionResourceNames(project, app.Stack, spec.Name)
		s.add(scope, tfLambdaFunction, spec.Name, map[string]any{
			"runtime":           args.Runtime,
			"memory_size":       args.MemorySizeMB,
			"timeout":           args.TimeoutSeconds,
			"architectures":     []any{args.Arch},
			"ephemeral_storage": map[string]any{"size": lambdaDefaultEphemeralMB},
		}, names["lambda"])
		s.add(scope, tfLogGroup, spec.Name, map[string]any{"retention_in_days": lambdaLogRetentionDays}, names["logGroup"])
		s.add(scope, tfLambdaFunctionURL, spec.Name, map[string]any{"invoke_mode": args.InvokeMode}, names["url"])
	}
	return nil
}

func (s shaper) container(scope string, app providerkit.AppEntry) {
	s.plain(scope, tfECSTaskDefinition, app.App, map[string]any{
		"cpu":                      containerCPU,
		"memory":                   containerMemory,
		"requires_compatibilities": []any{"FARGATE"},
		"runtime_platform":         map[string]any{"cpu_architecture": "X86_64", "operating_system_family": "LINUX"},
	})
	s.plain(scope, tfECSService, app.App, map[string]any{
		"desired_count":    fargateDesiredCount,
		"launch_type":      "FARGATE",
		"cpu":              containerCPU,
		"memory":           containerMemory,
		"runtime_platform": map[string]any{"cpu_architecture": "X86_64", "operating_system_family": "LINUX"},
	})
	s.plain(scope, tfECRRepository, app.App, map[string]any{"image_tag_mutability": "IMMUTABLE"})
}

func (s shaper) substrate(scope string, class providerkit.Class) {
	s.plain(scope, tfECSCluster, SubstrateSlug, map[string]any{})
	s.plain(scope, tfLoadBalancer, SubstrateSlug, map[string]any{"load_balancer_type": "application", "internal": false})
	s.plain(scope, tfLogGroup, SubstrateSlug, map[string]any{"retention_in_days": substrateLogRetentionDays, "name": "/ocel/containers/" + string(class)})
}

func (s shaper) postgres(scope, project, env string, resource providerkit.Resource) {
	args := translatePostgres(resource.Postgres)
	names := postgresResourceNames(project, env, resource.Name)
	s.add(scope, tfRDSCluster, resource.Name, map[string]any{
		"engine":         args.Engine,
		"engine_mode":    args.EngineMode,
		"engine_version": args.EngineVersion,
		"serverlessv2_scaling_configuration": map[string]any{
			"min_capacity": args.MinCapacity,
			"max_capacity": args.MaxCapacity,
		},
		"manage_master_user_password": args.ManageMasterPassword,
	}, names["cluster"])
	s.add(scope, tfRDSClusterInstance, resource.Name, map[string]any{
		"engine":         args.Engine,
		"instance_class": args.InstanceClass,
	}, names["instance"])
	s.plain(scope, tfSecret, resource.Name, map[string]any{"managed_by": "rds"})
}

func (s shaper) bucket(scope, project, env string, resource providerkit.Resource) {
	names := bucketResourceNames(project, env, resource.Name)
	s.add(scope, tfS3Bucket, resource.Name, map[string]any{}, names["bucket"])
	s.add(scope, tfLambdaFunction, resource.Name+"-"+uploadCompleterLocalName, map[string]any{
		"runtime":           uploadCompleterRuntime,
		"memory_size":       lambdaDefaultMemoryMB,
		"timeout":           uploadCompleterTimeoutSeconds,
		"architectures":     []any{providerkit.ArchX8664},
		"ephemeral_storage": map[string]any{"size": lambdaDefaultEphemeralMB},
	}, names["uploadCompleter"])
	s.add(scope, tfLogGroup, resource.Name+"-"+uploadCompleterLocalName, map[string]any{"retention_in_days": lambdaLogRetentionDays}, names["uploadCompleterLogGroup"])
}

func shapeTransforms(ctx context.Context, evaluator transform.Evaluator, project string, req providerkit.ShapeRequest) (shapedPatches, error) {
	held := shapedPatches{patches: map[resourceRef]map[string]any{}, unknown: map[resourceRef][]string{}}
	if evaluator == nil {
		return held, nil
	}
	request := transform.Request{Provider: transform.Provider, EnvClass: string(req.Plan.Class), Env: req.Plan.Env}
	var candidates []transformCandidate
	for _, resource := range req.Resources {
		if resource.Binding != "" {
			continue
		}
		switch resource.Type {
		case providerkit.BindingPostgres:
			request.Resources = append(request.Resources, transform.Resource{Type: transformTypePostgres, Name: resource.Name})
			candidates = append(candidates, transformCandidate{key: resourceKey{Type: transformTypePostgres, Name: resource.Name}, names: postgresResourceNames(project, req.Plan.Env, resource.Name)})
		case providerkit.BindingBucket:
			request.Resources = append(request.Resources, transform.Resource{Type: transformTypeBucket, Name: resource.Name})
			candidates = append(candidates, transformCandidate{key: resourceKey{Type: transformTypeBucket, Name: resource.Name}, names: bucketResourceNames(project, req.Plan.Env, resource.Name)})
		}
	}
	for _, app := range req.Plan.Apps {
		if app.Compute() == providerkit.ComputeContainer {
			continue
		}
		specs := req.Functions[app.App]
		if len(specs) == 0 {
			specs = []providerkit.FunctionSpec{{Name: app.App}}
		}
		for _, spec := range specs {
			request.Resources = append(request.Resources, transform.Resource{Type: transformTypeFunction, Name: spec.Name, App: app.App})
			candidates = append(candidates, transformCandidate{key: resourceKey{Type: transformTypeFunction, Name: spec.Name}, names: functionResourceNames(project, app.Stack, spec.Name)})
		}
	}
	if len(candidates) == 0 {
		return held, nil
	}
	results, err := evaluator.Evaluate(ctx, request)
	if err != nil {
		return held, err
	}
	if results == nil {
		return held, nil
	}
	if len(results) != len(candidates) {
		return held, providerkit.Refuse(providerkit.CodeInvalid, "transforms returned %d results for %d resources", len(results), len(candidates))
	}
	unresolved := map[outputSite]bool{}
	if err := walkOutputs(candidates, results, func(_ outputRef, at outputSite, authored any) (any, error) {
		unresolved[at] = true
		return authored, nil
	}); err != nil {
		return held, err
	}
	for i, candidate := range candidates {
		for _, key := range slices.Sorted(maps.Keys(results[i].Patches)) {
			ref, constructed := candidate.names[key]
			if !constructed {
				return held, providerkit.Refuse(providerkit.CodeInvalid,
					"a transform patches %s's %s, and this deploy constructs no such resource for it", candidate.key.Name, key)
			}
			for field, value := range results[i].Patches[key] {
				if unresolved[outputSite{Resource: candidate.key.Name, Surface: key, Field: field}] {
					held.unknown[ref] = append(held.unknown[ref], snake(field))
					continue
				}
				if held.patches[ref] == nil {
					held.patches[ref] = map[string]any{}
				}
				held.patches[ref][field] = value
			}
		}
	}
	return held, nil
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
