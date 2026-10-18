package deploy

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws/arn"
	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/appautoscaling"
	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/cloudwatch"
	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/ecs"
	iam "github.com/pulumi/pulumi-aws/sdk/v7/go/aws/iam"
	"github.com/pulumi/pulumi/sdk/v3/go/auto"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/processenv"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/resources"
	awsports "github.com/ocelhq/ocel/platform/aws/provider/ports"
	"github.com/ocelhq/ocel/platform/aws/provider/queues"
)

func appScopedName(at naming.Coordinate) string {
	scope := awsports.AppScope + naming.WordSeparator
	return scope + at.PhysicalName(maxLambdaNameLen-len(scope))
}

func containerWorkerService(project, env, app, worker string) string {
	return appScopedName(naming.Coordinate{Project: project, Env: env, App: app, Kind: naming.KindWorker, Name: worker})
}

func containerWorkers(tier environment.Tier, project, env string, workers []provider.WorkerSpec) map[string]queues.Worker {
	served := map[string]queues.Worker{}
	for _, worker := range workers {
		if worker.Compute != provider.ComputeContainer {
			continue
		}
		served[worker.Name] = queues.Worker{
			Concurrency: worker.Concurrency,
			Service:     &queues.Service{Cluster: awsports.ContainerClusterName(tier), Name: containerWorkerService(project, env, worker.App, worker.Name)},
		}
	}
	if len(served) == 0 {
		return nil
	}
	return served
}

func containerServiceARNs(cfg Config, project, env string, workers []provider.WorkerSpec) []string {
	var arns []string
	for _, worker := range containerWorkers(cfg.Tier, project, env, workers) {
		arns = append(arns, fmt.Sprintf("arn:aws:ecs:%s:%s:service/%s/%s", cfg.Region, accountOfARN(cfg.StateTableARN), worker.Service.Cluster, worker.Service.Name))
	}
	slices.Sort(arns)
	return arns
}

const (
	workerStopTimeoutSeconds  = 120
	backlogAlarmQueues        = 9
	backlogAlarmPeriodSeconds = 60
	backlogCooldownSeconds    = 60
	sqsNamespace              = "AWS/SQS"
	visibleMessagesMetric     = "ApproximateNumberOfMessagesVisible"
)

type workerDefinition struct {
	Name        string            `json:"name"`
	Image       string            `json:"image"`
	Essential   bool              `json:"essential"`
	Environment []environmentPair `json:"environment"`
	LogConfig   logConfiguration  `json:"logConfiguration"`
	StopTimeout int               `json:"stopTimeout"`
}

type containerWorkersWork struct {
	web         *containerWork
	project     string
	stack       naming.StackName
	workers     []provider.WorkerSpec
	topics      deployedTopics
	topology    string
	env         map[string]string
	floor       int
	account     string
	table       string
	prefix      string
	tasksPolicy string
	group       string
}

func (r *release) containerWorkersWork(spec provider.StackSpec, web *containerWork) (*containerWorkersWork, error) {
	app := spec.App
	project, env := naming.Sanitize(spec.Ref.Project), spec.Ref.Name.Env
	account := accountOfARN(r.cfg.StateTableARN)
	topics := topicsOf(project, env, app.Topics)
	served := containerWorkers(r.cfg.Tier, project, env, app.DeploymentWorkers)
	floor := workerFloor(app.App, app.Instances, app.Workers)
	for _, worker := range app.Workers {
		if hosted, found := served[worker.Name]; found {
			hosted.Service.Floor = floor
		}
	}
	topology, err := queueTopology(r.cfg, project, env, topics, served)
	if err != nil {
		return nil, err
	}
	rendered, err := queues.Render(topology)
	if err != nil {
		return nil, err
	}
	tasks, err := tasksPolicy(r.cfg, project, env, topics, containerServiceARNs(r.cfg, project, env, app.DeploymentWorkers))
	if err != nil {
		return nil, err
	}
	workerEnv := maps.Clone(web.env)
	delete(workerEnv, edge.OriginSecretVar)
	delete(workerEnv, edge.OriginSecretPreviousVar)
	workerEnv[queues.EnvVar] = string(rendered)
	workerEnv["AWS_REGION"] = r.cfg.Region
	return &containerWorkersWork{
		web:         web,
		project:     project,
		stack:       naming.ContainerWorkersStack(env, app.App),
		workers:     app.Workers,
		topics:      topics,
		topology:    string(rendered),
		env:         workerEnv,
		floor:       floor,
		account:     account,
		table:       r.cfg.StateTableARN,
		prefix:      naming.TaskKeyPrefix(project, env),
		tasksPolicy: tasks,
		group:       queues.ScheduleGroupName(project, env),
	}, nil
}

func workerFloor(app string, instances provider.Instances, workers []provider.WorkerSpec) int {
	if provider.IsWorkerApp(app, workers) {
		return instances.Min
	}
	return 0
}

func (w *containerWorkersWork) coordinate(kind naming.Kind, name string) naming.Coordinate {
	return naming.Coordinate{Project: w.project, Env: w.stack.Env, App: w.stack.App, Kind: kind, Name: name}
}

func (w *containerWorkersWork) run(ctx *pulumi.Context) error {
	role, granted, err := w.taskRole(ctx)
	if err != nil {
		return err
	}
	for _, worker := range w.workers {
		if err := w.declareWorker(ctx, worker, role, granted); err != nil {
			return err
		}
	}
	return w.declareCrons(ctx)
}

func (w *containerWorkersWork) declareCrons(ctx *pulumi.Context) error {
	var crons []cronTarget
	var queueARNs []string
	for _, worker := range w.workers {
		for _, task := range w.topics.cronTasks(worker.Name) {
			queue := task.queues[0]
			arn := sqsQueueARN(w.web.region, w.account, queue.name)
			scheduled := cronTarget{task: task, arn: pulumi.String(arn)}
			if queue.fifo {
				scheduled.messageGroup = cronMessageGroup
			}
			crons = append(crons, scheduled)
			queueARNs = append(queueARNs, arn)
		}
	}
	if len(crons) == 0 {
		return nil
	}
	policy, err := allowing("sqs:SendMessage", queueARNs)
	if err != nil {
		return err
	}
	assumed := schedulerRole{at: w.coordinate(naming.KindRole, "scheduler"), boundary: w.web.boundary, path: w.web.rolePath, tags: w.web.tags}
	return declareCrons(ctx, assumed, w.group, pulumi.String(policy), crons)
}

func (w *containerWorkersWork) taskRole(ctx *pulumi.Context) (*iam.Role, []pulumi.Resource, error) {
	at := w.coordinate(naming.KindRole, "worker-role")
	role, err := iam.NewRole(ctx, naming.ResourceID(naming.KindRole, roleLocalName), &iam.RoleArgs{
		NamePrefix:          pulumi.String(rolePrefix(at)),
		Description:         capDescription(at.Description("task role for this app's workers"), maxDescriptionLen),
		AssumeRolePolicy:    pulumi.String(assumeRolePolicy(ecsTasksPrincipal)),
		PermissionsBoundary: pulumi.String(w.web.boundary),
		Path:                pulumi.String(w.web.rolePath),
		Tags:                resourceTags(naming.KindRole, "", w.web.tags),
	})
	if err != nil {
		return nil, nil, err
	}
	consumption, err := consumptionPolicy(w.workers, w.topics, w.web.region, w.account, w.table, w.prefix)
	if err != nil {
		return nil, nil, err
	}
	documents := map[string]string{"queues": consumption, "tasks": w.tasksPolicy}
	for _, binding := range w.web.policies {
		documents["binding-"+binding.Binding] = binding.Policy
	}
	if w.web.values.ValuesTableARN != "" {
		if documents["variables"], err = variablesReadPolicy(w.web.values); err != nil {
			return nil, nil, err
		}
	}
	granted := make([]pulumi.Resource, 0, len(documents))
	for _, name := range slices.Sorted(maps.Keys(documents)) {
		policy, err := iam.NewRolePolicy(ctx, naming.ResourceID(naming.KindRole, roleLocalName, "policy", name), &iam.RolePolicyArgs{
			Role:   role.Name,
			Policy: pulumi.String(documents[name]),
		})
		if err != nil {
			return nil, nil, err
		}
		granted = append(granted, policy)
	}
	return role, granted, nil
}

func (w *containerWorkersWork) definition(worker provider.WorkerSpec, service string) (string, error) {
	env := maps.Clone(w.env)
	env[processenv.WorkerEnvVar] = worker.Name
	pairs := make([]environmentPair, 0, len(env))
	for _, key := range slices.Sorted(maps.Keys(env)) {
		pairs = append(pairs, environmentPair{Name: key, Value: env[key]})
	}
	encoded, err := json.Marshal([]workerDefinition{{
		Name:        containerName,
		Image:       w.web.image,
		Essential:   true,
		Environment: pairs,
		StopTimeout: workerStopTimeoutSeconds,
		LogConfig: logConfiguration{
			Driver: "awslogs",
			Options: map[string]string{
				"awslogs-group":         w.web.infra.LogGroup,
				"awslogs-region":        w.web.region,
				"awslogs-stream-prefix": service,
			},
		},
	}})
	if err != nil {
		return "", fmt.Errorf("render worker %s's container definition: %w", worker.Name, err)
	}
	return string(encoded), nil
}

func (w *containerWorkersWork) declareWorker(ctx *pulumi.Context, worker provider.WorkerSpec, role *iam.Role, granted []pulumi.Resource) error {
	service := containerWorkerService(w.project, w.stack.Env, w.stack.App, worker.Name)
	tags := resourceTags(naming.KindWorker, "", w.web.tags)
	definition, err := w.definition(worker, service)
	if err != nil {
		return err
	}
	task, err := ecs.NewTaskDefinition(ctx, naming.ResourceID(naming.KindWorker, worker.Name, "task"), &ecs.TaskDefinitionArgs{
		Family:                  pulumi.String(service),
		Cpu:                     pulumi.String(containerCPU),
		Memory:                  pulumi.String(containerMemory),
		NetworkMode:             pulumi.String("awsvpc"),
		RequiresCompatibilities: pulumi.StringArray{pulumi.String("FARGATE")},
		ExecutionRoleArn:        pulumi.String(w.web.infra.ExecutionRole),
		TaskRoleArn:             role.Arn,
		ContainerDefinitions:    pulumi.ToSecret(pulumi.String(definition)).(pulumi.StringOutput),
		RuntimePlatform: &ecs.TaskDefinitionRuntimePlatformArgs{
			OperatingSystemFamily: pulumi.String("LINUX"),
			CpuArchitecture:       pulumi.String(fargateCPUArchitecture(w.web.arch)),
		},
		Tags: tags,
	}, pulumi.DependsOn(granted))
	if err != nil {
		return err
	}
	running, err := ecs.NewService(ctx, naming.ResourceID(naming.KindWorker, worker.Name, "service"), &ecs.ServiceArgs{
		Name:           pulumi.String(service),
		Cluster:        pulumi.String(w.web.infra.Cluster),
		TaskDefinition: task.Arn,
		DesiredCount:   pulumi.Int(w.floor),
		LaunchType:     pulumi.String("FARGATE"),
		NetworkConfiguration: &ecs.ServiceNetworkConfigurationArgs{
			Subnets:        pulumi.ToStringArray(w.web.infra.Subnets),
			SecurityGroups: pulumi.StringArray{pulumi.String(w.web.infra.TaskSecurity)},
			AssignPublicIp: pulumi.Bool(true),
		},
		Tags: tags,
	}, pulumi.IgnoreChanges([]string{"desiredCount"}))
	if err != nil {
		return err
	}
	if err := w.scaleOnBacklog(ctx, worker, running, tags); err != nil {
		return err
	}
	ctx.Export(resources.WorkerName(worker.Name), pulumi.Map{outputKeyContainerPhysical: running.Name})
	return nil
}

func (w *containerWorkersWork) scaleOnBacklog(ctx *pulumi.Context, worker provider.WorkerSpec, service *ecs.Service, tags pulumi.StringMap) error {
	clusterARN, err := arn.Parse(w.web.infra.Cluster)
	cluster, found := strings.CutPrefix(clusterARN.Resource, "cluster/")
	if err != nil || !found {
		return fmt.Errorf("the container cluster %q is not an ECS cluster ARN, so worker %s's service has no scalable target to name", w.web.infra.Cluster, worker.Name)
	}
	target, err := appautoscaling.NewTarget(ctx, naming.ResourceID(naming.KindWorker, worker.Name, "scaling"), &appautoscaling.TargetArgs{
		ServiceNamespace:  pulumi.String(scalingNamespace),
		ScalableDimension: pulumi.String(scalingDimension),
		ResourceId:        pulumi.Sprintf("service/%s/%s", cluster, service.Name),
		MinCapacity:       pulumi.Int(w.floor),
		MaxCapacity:       pulumi.Int(max(w.web.instances.Max, w.floor, 1)),
		Tags:              tags,
	})
	if err != nil {
		return err
	}
	out, err := appautoscaling.NewPolicy(ctx, naming.ResourceID(naming.KindWorker, worker.Name, "scaling", "backlog"), &appautoscaling.PolicyArgs{
		PolicyType:        pulumi.String("StepScaling"),
		ServiceNamespace:  target.ServiceNamespace,
		ScalableDimension: target.ScalableDimension,
		ResourceId:        target.ResourceId,
		StepScalingPolicyConfiguration: &appautoscaling.PolicyStepScalingPolicyConfigurationArgs{
			AdjustmentType:        pulumi.String("ChangeInCapacity"),
			Cooldown:              pulumi.Int(backlogCooldownSeconds),
			MetricAggregationType: pulumi.String("Maximum"),
			StepAdjustments: appautoscaling.PolicyStepScalingPolicyConfigurationStepAdjustmentArray{
				&appautoscaling.PolicyStepScalingPolicyConfigurationStepAdjustmentArgs{MetricIntervalLowerBound: pulumi.String("0"), ScalingAdjustment: pulumi.Int(1)},
			},
		},
	})
	if err != nil {
		return err
	}
	var names []string
	for _, queue := range w.topics.served(worker.Name) {
		names = append(names, queue.name)
	}
	for at, group := range slices.Collect(slices.Chunk(names, backlogAlarmQueues)) {
		if err := w.declareBacklogAlarm(ctx, worker, at, group, out, tags); err != nil {
			return err
		}
	}
	return nil
}

func (w *containerWorkersWork) declareBacklogAlarm(ctx *pulumi.Context, worker provider.WorkerSpec, at int, queueNames []string, out *appautoscaling.Policy, tags pulumi.StringMap) error {
	name := appScopedName(w.coordinate(naming.KindWorker, worker.Name+"-backlog-"+strconv.Itoa(at)))
	queries := cloudwatch.MetricAlarmMetricQueryArray{}
	ids := make([]string, 0, len(queueNames))
	for i, queue := range queueNames {
		id := "m" + strconv.Itoa(i)
		ids = append(ids, id)
		queries = append(queries, &cloudwatch.MetricAlarmMetricQueryArgs{
			Id: pulumi.String(id),
			Metric: &cloudwatch.MetricAlarmMetricQueryMetricArgs{
				Namespace:  pulumi.String(sqsNamespace),
				MetricName: pulumi.String(visibleMessagesMetric),
				Dimensions: pulumi.StringMap{"QueueName": pulumi.String(queue)},
				Period:     pulumi.Int(backlogAlarmPeriodSeconds),
				Stat:       pulumi.String("Maximum"),
			},
		})
	}
	queries = append(queries, &cloudwatch.MetricAlarmMetricQueryArgs{
		Id:         pulumi.String("backlog"),
		Expression: pulumi.String(strings.Join(ids, "+")),
		Label:      pulumi.String("visible messages"),
		ReturnData: pulumi.Bool(true),
	})
	_, err := cloudwatch.NewMetricAlarm(ctx, naming.ResourceID(naming.KindWorker, worker.Name, "backlog", strconv.Itoa(at)), &cloudwatch.MetricAlarmArgs{
		Name:               pulumi.String(name),
		AlarmDescription:   capDescription(w.coordinate(naming.KindWorker, worker.Name).Description("starts the worker when a queue it serves holds a message"), maxDescriptionLen),
		ComparisonOperator: pulumi.String("GreaterThanThreshold"),
		Threshold:          pulumi.Float64(0),
		EvaluationPeriods:  pulumi.Int(1),
		DatapointsToAlarm:  pulumi.Int(1),
		TreatMissingData:   pulumi.String("notBreaching"),
		MetricQueries:      queries,
		AlarmActions:       pulumi.Array{out.Arn},
		Tags:               tags,
	})
	return err
}

func (w *containerWorkersWork) decode(outputs auto.OutputMap) (provider.StackResult, error) {
	containers := make([]provider.AppContainer, 0, len(w.workers))
	for _, worker := range w.workers {
		logical := resources.WorkerName(worker.Name)
		fields, err := requireOutputFields(outputs, logical)
		if err != nil {
			return provider.StackResult{}, err
		}
		physical, err := requireStringField(fields, logical, outputKeyContainerPhysical)
		if err != nil {
			return provider.StackResult{}, err
		}
		containers = append(containers, provider.AppContainer{Name: logical, Physical: physical, Image: w.web.image})
	}
	return provider.StackResult{Containers: containers}, nil
}

func hostsContainerWorkers(spec provider.StackSpec) bool {
	return spec.App != nil && len(spec.App.Workers) > 0 && spec.Ref.Name.Env != ""
}

func (r *release) previewContainerWorkers(ctx context.Context, spec provider.StackSpec, web *containerWork, progress progress.Log) ([]provider.ChangeGroup, error) {
	if !hostsContainerWorkers(spec) {
		return nil, nil
	}
	hosted, err := r.containerWorkersWork(spec, web)
	if err != nil {
		return nil, err
	}
	ref := containerWorkersRef(spec.Ref)
	planned, err := r.automation.Preview(ctx, ref, stackProgram{run: hosted.run, tags: spec.Tags}, spec.Lease, progress)
	return planned.Groups, err
}

func (r *release) provisionContainerWorkers(ctx context.Context, spec provider.StackSpec, web *containerWork, progress progress.Log) ([]provider.AppContainer, error) {
	if !hostsContainerWorkers(spec) {
		return nil, nil
	}
	hosted, err := r.containerWorkersWork(spec, web)
	if err != nil {
		return nil, err
	}
	ref := containerWorkersRef(spec.Ref)
	if err := r.recordWorkers(ctx, ref); err != nil {
		return nil, fmt.Errorf("record the workers of %s: %w", spec.App.App, err)
	}
	if r.cfg.KeyValues != nil {
		if err := r.recordContainerInfraConsumer(ctx, ref); err != nil {
			return nil, err
		}
	}
	outputs, err := r.automation.Run(ctx, ref, stackProgram{run: hosted.run, tags: spec.Tags}, spec.Lease, progress)
	if err != nil {
		return nil, fmt.Errorf("provision the workers of %s: %w", spec.App.App, err)
	}
	result, err := hosted.decode(outputs)
	if err != nil {
		return nil, fmt.Errorf("provision the workers of %s: %w", spec.App.App, err)
	}
	return result.Containers, nil
}
