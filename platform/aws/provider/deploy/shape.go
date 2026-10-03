package deploy

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"unicode"

	"github.com/ocelhq/ocel/pkg/arch"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/pricing"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/transform"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/aws/provider/logevents"
)

const (
	vendor = "aws"

	tfLambdaFunction     = "aws_lambda_function"
	tfLambdaFunctionURL  = "aws_lambda_function_url"
	tfLogGroup           = "aws_cloudwatch_log_group"
	tfAppSyncAPI         = "aws_appsync_api"
	tfS3Bucket           = "aws_s3_bucket"
	tfRDSCluster         = "aws_rds_cluster"
	tfRDSClusterInstance = "aws_rds_cluster_instance"
	tfSecret             = "aws_secretsmanager_secret"
	tfECSCluster         = "aws_ecs_cluster"
	tfECSTaskDefinition  = "aws_ecs_task_definition"
	tfECSService         = "aws_ecs_service"
	tfLoadBalancer       = "aws_lb"
	tfECRRepository      = "aws_ecr_repository"

	tfElastiCacheReplicationGroup = "aws_elasticache_replication_group"
	tfElastiCacheParameterGroup   = "aws_elasticache_parameter_group"
	tfElastiCacheSubnetGroup      = "aws_elasticache_subnet_group"
	tfSSMParameter                = "aws_ssm_parameter"

	tfSQSQueue           = "aws_sqs_queue"
	tfSNSTopic           = "aws_sns_topic"
	tfEventSourceMapping = "aws_lambda_event_source_mapping"
	tfSchedulerSchedule  = "aws_scheduler_schedule"

	lambdaDefaultMemoryMB    = 128
	lambdaDefaultEphemeralMB = 512
)

type ShapeScopes struct {
	Environment string
	Shared      string
}

type shapedPatches struct {
	patches map[resourceRef]map[string]any
	unknown map[resourceRef][]string
}

func Shape(ctx context.Context, pass transform.Pass, region string, req provider.ShapeRequest, tree *pricing.Tree, scopes ShapeScopes) error {
	project := naming.Sanitize(req.Deploy.Slug)
	patched, err := shapeTransforms(ctx, pass, project, req)
	if err != nil {
		return err
	}
	shape := costShape{tree: tree, region: region, patched: patched}
	for _, resource := range req.Resources {
		if resource.Binding != "" {
			continue
		}
		switch resource.Type {
		case provider.BindingPostgres:
			shape.postgres(scopes.Environment, project, req.Deploy.Env, resource)
		case provider.BindingBucket:
			shape.bucket(scopes.Environment, project, req.Deploy.Env, resource)
		case provider.BindingKV:
			if err := shape.kv(scopes.Environment, project, req.Deploy.Env, resource); err != nil {
				return err
			}
		}
	}
	shape.realtime(scopes.Environment, req.Resources)
	topics := topicsOf(project, req.Deploy.Env, req.Resources)
	shape.topics(scopes.Environment, topics)
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
		shape.containerInfra(scopes.Shared, req.Deploy.Tier)
	}
	return nil
}

type costShape struct {
	tree    *pricing.Tree
	region  string
	patched shapedPatches
}

func (s costShape) add(scope, typ, name string, properties map[string]any, ref resourceRef) {
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

func (s costShape) plain(scope, typ, name string, properties map[string]any) {
	s.tree.Add(scope, vendor, typ, name, s.region, properties)
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
		args, err := translateFunctionSpec(framework, spec)
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

func (s costShape) topics(scope string, topics []deployedTopic) {
	for _, topic := range topics {
		if topic.sns != "" {
			s.plain(scope, tfSNSTopic, topic.resource.Declared, map[string]any{"fifo_topic": topic.declared.Ordered})
		}
		for _, queue := range topic.queues {
			s.plain(scope, tfSQSQueue, queue.topic+"/"+queue.consumer.Name, map[string]any{"fifo_queue": queue.fifo})
			s.plain(scope, tfSQSQueue, queue.topic+"/"+queue.consumer.Name+"/dead-letters", map[string]any{"fifo_queue": queue.fifo})
		}
	}
}

func (s costShape) workers(environment string, req provider.ShapeRequest, topics []deployedTopic) error {
	if req.Deploy.Infra.IsZero() {
		return nil
	}
	for _, app := range req.Deploy.Apps {
		if len(app.Workers) == 0 || app.Compute() != provider.ComputeServerless {
			continue
		}
		if err := s.appWorkers(environment, req, app, topics); err != nil {
			return err
		}
	}
	return nil
}

func (s costShape) appWorkers(environment string, req provider.ShapeRequest, app provider.AppEntry, topics []deployedTopic) error {
	scope := s.tree.Scope(environment, pricing.ScopeApp, app.App)
	args, err := workerFunctionArgs(app, req.Functions[app.App])
	if err != nil {
		return err
	}
	hosted := &workersWork{topics: topics}
	for _, worker := range app.Workers {
		s.plain(scope, tfLambdaFunction, worker.Name, map[string]any{
			"runtime":           args.Runtime,
			"memory_size":       args.MemorySizeMB,
			"timeout":           int(workerTimeout(topics, worker.Name).Seconds()),
			"architectures":     []any{args.Arch},
			"ephemeral_storage": map[string]any{"size": lambdaDefaultEphemeralMB},
		})
		s.plain(scope, tfLogGroup, worker.Name, map[string]any{"retention_in_days": lambdaLogRetentionDays})
		for _, queue := range hosted.served(worker.Name) {
			s.plain(scope, tfEventSourceMapping, queue.topic+"/"+queue.consumer.Name, map[string]any{"batch_size": batchSize(queue.consumer)})
		}
		for _, task := range hosted.cronTasks(worker.Name) {
			expressions, err := schedulerExpressions(task.declared.Cron)
			if err != nil {
				return fmt.Errorf("task %s: %w", task.resource.Declared, err)
			}
			for _, expression := range expressions {
				s.plain(scope, tfSchedulerSchedule, task.resource.Declared, map[string]any{"schedule_expression": expression})
			}
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

func workerFunctionArgs(app provider.AppEntry, specs []provider.FunctionSpec) (functionArgs, error) {
	framework := app.Manifest.GetFramework().GetName()
	spec := provider.FunctionSpec{Name: app.App}
	if len(specs) > 0 {
		spec = specs[0]
	}
	if spec.Framework.Name == "" {
		spec.Framework.Name = framework
	}
	if spec.Framework.Arch == "" {
		spec.Framework.Arch = arch.Architecture(app.Manifest.GetFramework().GetArch())
	}
	return translateFunctionSpec(framework, spec)
}

func (s costShape) container(scope string, app provider.AppEntry) {
	s.plain(scope, tfECSTaskDefinition, app.App, map[string]any{
		"cpu":                      containerCPU,
		"memory":                   containerMemory,
		"requires_compatibilities": []any{"FARGATE"},
		"runtime_platform":         map[string]any{"cpu_architecture": fargateCPUArchitecture(app.Arch), "operating_system_family": "LINUX"},
	})
	s.plain(scope, tfECSService, app.App, map[string]any{
		"desired_count":    app.Instances.Min,
		"launch_type":      "FARGATE",
		"cpu":              containerCPU,
		"memory":           containerMemory,
		"runtime_platform": map[string]any{"cpu_architecture": fargateCPUArchitecture(app.Arch), "operating_system_family": "LINUX"},
	})
	s.plain(scope, tfECRRepository, app.App, map[string]any{"image_tag_mutability": "IMMUTABLE"})
}

func (s costShape) containerInfra(scope string, tier environment.Tier) {
	s.plain(scope, tfECSCluster, ContainersSlug, map[string]any{})
	s.plain(scope, tfLoadBalancer, ContainersSlug, map[string]any{"load_balancer_type": "application", "internal": true})
	s.plain(scope, tfLogGroup, ContainersSlug, map[string]any{"retention_in_days": containerLogRetentionDays, "name": logevents.NameContainerGroup(tier)})
}

func (s costShape) postgres(scope, project, env string, resource provider.Resource) {
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
		"serverlessv2_scaling_configuration": map[string]any{
			"min_capacity": args.MinCapacity,
			"max_capacity": args.MaxCapacity,
		},
	}, names["instance"])
	s.plain(scope, tfSecret, resource.Name, map[string]any{"managed_by": "rds"})
}

func (s costShape) kv(scope, project, env string, resource provider.Resource) error {
	args, err := translateKV(resource.Name, resource.KV)
	if err != nil {
		return err
	}
	names := kvResourceNames(project, env, resource.Name)
	s.add(scope, tfElastiCacheReplicationGroup, resource.Name, map[string]any{
		"engine":                     args.Engine,
		"engine_version":             args.EngineVersion,
		"node_type":                  args.NodeType,
		"num_cache_clusters":         args.NumCacheClusters,
		"automatic_failover_enabled": args.AutomaticFailoverEnabled,
		"multi_az_enabled":           args.MultiAZEnabled,
		"snapshot_retention_limit":   args.SnapshotRetentionLimit,
	}, names["replicationGroup"])
	s.add(scope, tfElastiCacheParameterGroup, resource.Name, map[string]any{"family": args.Family}, names["parameterGroup"])
	s.add(scope, tfElastiCacheSubnetGroup, resource.Name, map[string]any{}, names["subnetGroup"])
	s.plain(scope, tfSSMParameter, resource.Name, map[string]any{"type": "SecureString"})
	return nil
}

func (s costShape) realtime(scope string, resources []provider.Resource) {
	realtimes := realtimeResourcesOf(resources)
	if len(realtimes) == 0 {
		return
	}
	authorizer := string(naming.KindRealtimeAuthorizer)
	s.plain(scope, tfAppSyncAPI, string(naming.KindRealtime), map[string]any{})
	s.plain(scope, tfLambdaFunction, authorizer, map[string]any{
		"runtime":           realtimeAuthorizerRuntime,
		"memory_size":       realtimeAuthorizerMemoryMB,
		"timeout":           realtimeAuthorizerTimeoutSeconds,
		"architectures":     []any{arch.ARM64},
		"ephemeral_storage": map[string]any{"size": lambdaDefaultEphemeralMB},
	})
	s.plain(scope, tfLogGroup, authorizer, map[string]any{"retention_in_days": lambdaLogRetentionDays})
	for _, resource := range realtimes {
		s.plain(scope, tfSecret, resource.Declared, map[string]any{"managed_by": "ocel"})
	}
}

func (s costShape) bucket(scope, project, env string, resource provider.Resource) {
	names := bucketResourceNames(project, env, resource.Name)
	s.add(scope, tfS3Bucket, resource.Name, map[string]any{}, names["bucket"])
	s.add(scope, tfLambdaFunction, resource.Name+"-"+uploadCompleterLocalName, map[string]any{
		"runtime":           uploadCompleterRuntime,
		"memory_size":       lambdaDefaultMemoryMB,
		"timeout":           uploadCompleterTimeoutSeconds,
		"architectures":     []any{arch.X8664},
		"ephemeral_storage": map[string]any{"size": lambdaDefaultEphemeralMB},
	}, names["uploadCompleter"])
	s.add(scope, tfLogGroup, resource.Name+"-"+uploadCompleterLocalName, map[string]any{"retention_in_days": lambdaLogRetentionDays}, names["uploadCompleterLogGroup"])
}

func shapeTransforms(ctx context.Context, pass transform.Pass, project string, req provider.ShapeRequest) (shapedPatches, error) {
	collected := shapedPatches{patches: map[resourceRef]map[string]any{}, unknown: map[resourceRef][]string{}}
	if pass == nil {
		return collected, nil
	}
	request := transform.Request{Provider: transformProvider, EnvTier: string(req.Deploy.Tier), Env: req.Deploy.Env}
	var candidates []transformCandidate
	for _, resource := range req.Resources {
		if resource.Binding != "" {
			continue
		}
		if shown, candidate, patchable := newInfraTransformCandidate(project, req.Deploy.Env, resource); patchable {
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
			candidates = append(candidates, transformCandidate{key: resourceKey{Type: transformTypeFunction, Name: spec.Name}, names: functionResourceNames(project, app.Stack, spec.Name)})
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
