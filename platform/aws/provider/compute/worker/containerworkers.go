package worker

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws/arn"
	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/processenv"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/resources"
	"github.com/ocelhq/ocel/platform/aws/provider/component"
	"github.com/ocelhq/ocel/platform/aws/provider/compute/container"
	"github.com/ocelhq/ocel/platform/aws/provider/iam"
	"github.com/ocelhq/ocel/platform/aws/provider/queues"
	"github.com/ocelhq/ocel/platform/aws/provider/resources/task"
	"github.com/ocelhq/ocel/platform/aws/provider/resources/topic"
	"github.com/ocelhq/ocel/platform/aws/provider/resources/variables"
	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/appautoscaling"
	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/cloudwatch"
	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/ecs"
	awsiam "github.com/pulumi/pulumi-aws/sdk/v7/go/aws/iam"
	"github.com/pulumi/pulumi/sdk/v3/go/auto"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

const (
	workerStopTimeoutSeconds  = 120
	BacklogAlarmQueues        = 9
	backlogAlarmPeriodSeconds = 60
	backlogCooldownSeconds    = 60
	sqsNamespace              = "AWS/SQS"
	visibleMessagesMetric     = "ApproximateNumberOfMessagesVisible"
	maxAlarmNameLen           = 255

	workerContainerRoleWord    = "container-worker-role"
	schedulerContainerRoleWord = "container-scheduler-role"
	scalingPart                = "scaling"
	backlogWord                = "backlog"
)

type workerDefinition struct {
	Name        string                      `json:"name"`
	Image       string                      `json:"image"`
	Essential   bool                        `json:"essential"`
	Environment []container.EnvironmentPair `json:"environment"`
	LogConfig   container.LogConfiguration  `json:"logConfiguration"`
	StopTimeout int                         `json:"stopTimeout"`
}

type ContainerStack struct {
	names       queues.Names
	web         *container.Definition
	project     string
	stack       naming.StackName
	workers     []provider.WorkerSpec
	topics      topic.Topics
	topology    string
	env         map[string]string
	floor       int
	account     string
	table       string
	prefix      string
	tasksPolicy string
	group       string
}

func NewContainerStack(spec provider.StackSpec, web *container.Definition, target Target) (*ContainerStack, error) {
	app := spec.App
	project, env := naming.Sanitize(spec.Ref.Project), spec.Ref.Name.Env
	account := component.AccountOfARN(target.StateTable.ARN)
	names := queues.Names{Namespace: target.Namespace, Project: project, Env: env}
	topics, err := topic.Of(names, app.Topics)
	if err != nil {
		return nil, err
	}
	served, err := container.WorkerServices(target.Namespace, target.Tier, project, env, app.DeploymentWorkers)
	if err != nil {
		return nil, err
	}
	group, err := names.ScheduleGroup()
	if err != nil {
		return nil, err
	}
	floor := Floor(app.App, app.Instances, app.Workers)
	for _, worker := range app.Workers {
		if hosted, found := served[worker.Name]; found {
			hosted.Service.Floor = floor
		}
	}
	topology, err := topic.QueueTopology(target.StateTable, project, env, topics, served)
	if err != nil {
		return nil, err
	}
	rendered, err := queues.Render(topology)
	if err != nil {
		return nil, err
	}
	wakes, err := container.WorkerServiceARNs(target.StateTable, target.Namespace, target.Tier, project, env, app.DeploymentWorkers)
	if err != nil {
		return nil, err
	}
	tasks, err := topic.TasksPolicy(target.StateTable, project, env, topics, wakes)
	if err != nil {
		return nil, err
	}
	workerEnv := maps.Clone(web.Env)
	delete(workerEnv, edge.OriginFrontedVar)
	delete(workerEnv, edge.OriginSecretVar)
	delete(workerEnv, edge.OriginSecretPreviousVar)
	workerEnv[queues.EnvVar] = string(rendered)
	workerEnv["AWS_REGION"] = target.StateTable.Region
	return &ContainerStack{
		names:       names,
		web:         web,
		project:     project,
		stack:       naming.ContainerWorkersStack(env, app.App),
		workers:     app.Workers,
		topics:      topics,
		topology:    string(rendered),
		env:         workerEnv,
		floor:       floor,
		account:     account,
		table:       target.StateTable.ARN,
		prefix:      naming.TaskKeyPrefix(project, env),
		tasksPolicy: tasks,
		group:       group,
	}, nil
}

func Floor(app string, instances provider.Instances, workers []provider.WorkerSpec) int {
	if provider.IsWorkerApp(app, workers) {
		return instances.Min
	}
	return 0
}

func (w *ContainerStack) coordinate(kind naming.Kind, name string) naming.Coordinate {
	return naming.Coordinate{Project: w.project, Env: w.stack.Env, App: w.stack.App, Kind: kind, Name: name}
}

func (w *ContainerStack) Declare(ctx *pulumi.Context) error {
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

func (w *ContainerStack) declareCrons(ctx *pulumi.Context) error {
	var crons []task.CronTarget
	var queueARNs []string
	for _, worker := range w.workers {
		for _, cronTask := range w.topics.CronTasks(worker.Name) {
			queue := cronTask.Queues[0]
			arn := topic.SQSQueueARN(w.web.Region, w.account, queue.Name)
			scheduled := task.CronTarget{Task: cronTask, ARN: pulumi.String(arn)}
			if queue.FIFO {
				scheduled.MessageGroup = task.CronMessageGroup
			}
			crons = append(crons, scheduled)
			queueARNs = append(queueARNs, arn)
		}
	}
	if len(crons) == 0 {
		return nil
	}
	policy, err := task.Allowing("sqs:SendMessage", queueARNs)
	if err != nil {
		return err
	}
	schedulerAt := w.coordinate(naming.KindRole, schedulerContainerRoleWord)
	scheduler, err := component.PhysicalName(w.names.Namespace, schedulerAt, component.MaxRoleNameLen)
	if err != nil {
		return err
	}
	assumed := task.SchedulerRole{Name: scheduler, At: schedulerAt, Boundary: w.web.Boundary, Path: w.web.RolePath, Tags: w.web.Tags}
	return task.DeclareCrons(ctx, assumed, w.names, w.group, pulumi.String(policy), crons)
}

func (w *ContainerStack) taskRole(ctx *pulumi.Context) (*awsiam.Role, []pulumi.Resource, error) {
	at := w.coordinate(naming.KindRole, workerContainerRoleWord)
	physical, err := component.PhysicalName(w.names.Namespace, at, component.MaxRoleNameLen)
	if err != nil {
		return nil, nil, err
	}
	consumption, err := consumptionPolicy(w.workers, w.topics, w.web.Region, w.account, w.table, w.prefix)
	if err != nil {
		return nil, nil, err
	}
	documents := map[string]pulumi.StringInput{queuesPolicyName: pulumi.String(consumption), component.TasksPolicyName: pulumi.String(w.tasksPolicy)}
	for _, binding := range w.web.Policies {
		documents[component.BindingPolicyName(binding.Binding)] = pulumi.String(binding.Policy)
	}
	if w.web.Variables.TableARN != "" {
		document, err := variables.ReadPolicy(w.web.Variables)
		if err != nil {
			return nil, nil, err
		}
		documents[component.VariablesPolicyName] = pulumi.String(document)
	}
	return component.NewAppRole(ctx, component.AppRole{
		Name:        component.NameOf(physical),
		Description: component.CapDescription(at.Description("task role for this app's workers"), component.MaxDescriptionLen),
		Principal:   iam.ECSTasksPrincipal,
		Boundary:    w.web.Boundary,
		Path:        w.web.RolePath,
		Tags:        w.web.Tags,
		Managed:     w.web.ManagedPolicies,
		Policies:    documents,
	})
}

func (w *ContainerStack) definition(worker provider.WorkerSpec, service string) (string, error) {
	env := maps.Clone(w.env)
	env[processenv.WorkerEnvVar] = worker.Name
	pairs := make([]container.EnvironmentPair, 0, len(env))
	for _, key := range slices.Sorted(maps.Keys(env)) {
		pairs = append(pairs, container.EnvironmentPair{Name: key, Value: env[key]})
	}
	encoded, err := json.Marshal([]workerDefinition{{
		Name:        container.Name,
		Image:       w.web.Image,
		Essential:   true,
		Environment: pairs,
		StopTimeout: workerStopTimeoutSeconds,
		LogConfig: container.LogConfiguration{
			Driver: "awslogs",
			Options: map[string]string{
				"awslogs-group":         w.web.Infra.LogGroup,
				"awslogs-region":        w.web.Region,
				"awslogs-stream-prefix": service,
			},
		},
	}})
	if err != nil {
		return "", fmt.Errorf("render worker %s's container definition: %w", worker.Name, err)
	}
	return string(encoded), nil
}

func (w *ContainerStack) declareWorker(ctx *pulumi.Context, worker provider.WorkerSpec, role *awsiam.Role, granted []pulumi.Resource) error {
	service, err := container.WorkerServiceName(w.names.Namespace, w.project, w.stack.Env, w.stack.App, worker.Name)
	if err != nil {
		return err
	}
	tags := component.Tags(naming.KindWorker, "", w.web.Tags)
	definition, err := w.definition(worker, service)
	if err != nil {
		return err
	}
	task, err := ecs.NewTaskDefinition(ctx, service, &ecs.TaskDefinitionArgs{
		Family:                  pulumi.String(service),
		Cpu:                     pulumi.String(container.CPU),
		Memory:                  pulumi.String(container.Memory),
		NetworkMode:             pulumi.String("awsvpc"),
		RequiresCompatibilities: pulumi.StringArray{pulumi.String("FARGATE")},
		ExecutionRoleArn:        pulumi.String(w.web.Infra.ExecutionRole),
		TaskRoleArn:             role.Arn,
		ContainerDefinitions:    pulumi.ToSecret(pulumi.String(definition)).(pulumi.StringOutput),
		RuntimePlatform: &ecs.TaskDefinitionRuntimePlatformArgs{
			OperatingSystemFamily: pulumi.String("LINUX"),
			CpuArchitecture:       pulumi.String(container.CPUArchitecture(w.web.Arch)),
		},
		Tags: tags,
	}, pulumi.DependsOn(granted))
	if err != nil {
		return err
	}
	running, err := ecs.NewService(ctx, service, &ecs.ServiceArgs{
		Name:           pulumi.String(service),
		Cluster:        pulumi.String(w.web.Infra.Cluster),
		TaskDefinition: task.Arn,
		DesiredCount:   pulumi.Int(w.floor),
		LaunchType:     pulumi.String("FARGATE"),
		NetworkConfiguration: &ecs.ServiceNetworkConfigurationArgs{
			Subnets:        pulumi.ToStringArray(w.web.Infra.Subnets),
			SecurityGroups: w.web.TaskSecurityGroups(),
			AssignPublicIp: pulumi.Bool(true),
		},
		Tags: tags,
	}, pulumi.IgnoreChanges([]string{"desiredCount"}))
	if err != nil {
		return err
	}
	if err := w.scaleOnBacklog(ctx, worker, service, running, tags); err != nil {
		return err
	}
	ctx.Export(resources.WorkerName(worker.Name), pulumi.Map{container.OutputKeyPhysical: running.Name})
	return nil
}

func (w *ContainerStack) scaleOnBacklog(ctx *pulumi.Context, worker provider.WorkerSpec, physical string, service *ecs.Service, tags pulumi.StringMap) error {
	clusterARN, err := arn.Parse(w.web.Infra.Cluster)
	cluster, found := strings.CutPrefix(clusterARN.Resource, "cluster/")
	if err != nil || !found {
		return fmt.Errorf("the container cluster %q is not an ECS cluster ARN, so worker %s's service has no scalable target to name", w.web.Infra.Cluster, worker.Name)
	}
	target, err := appautoscaling.NewTarget(ctx, physical+component.PartSeparator+scalingPart, &appautoscaling.TargetArgs{
		ServiceNamespace:  pulumi.String(container.ScalingNamespace),
		ScalableDimension: pulumi.String(container.ScalingDimension),
		ResourceId:        pulumi.Sprintf("service/%s/%s", cluster, service.Name),
		MinCapacity:       pulumi.Int(w.floor),
		MaxCapacity:       pulumi.Int(max(w.web.Instances.Max, w.floor, 1)),
		Tags:              tags,
	})
	if err != nil {
		return err
	}
	out, err := appautoscaling.NewPolicy(ctx, physical+component.PartSeparator+scalingPart+naming.WordSeparator+backlogWord, &appautoscaling.PolicyArgs{
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
	for _, queue := range w.topics.Served(worker.Name) {
		names = append(names, queue.Name)
	}
	for at, group := range slices.Collect(slices.Chunk(names, BacklogAlarmQueues)) {
		if err := w.declareBacklogAlarm(ctx, worker, at, group, out, tags); err != nil {
			return err
		}
	}
	return nil
}

func (w *ContainerStack) declareBacklogAlarm(ctx *pulumi.Context, worker provider.WorkerSpec, at int, queueNames []string, out *appautoscaling.Policy, tags pulumi.StringMap) error {
	name, err := component.PhysicalName(w.names.Namespace, w.coordinate(naming.KindWorker, naming.Join(naming.WordSeparator, worker.Name, backlogWord, strconv.Itoa(at))), maxAlarmNameLen)
	if err != nil {
		return err
	}
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
	_, err = cloudwatch.NewMetricAlarm(ctx, name, &cloudwatch.MetricAlarmArgs{
		Name:               pulumi.String(name),
		AlarmDescription:   component.CapDescription(w.coordinate(naming.KindWorker, worker.Name).Description("starts the worker when a queue it serves holds a message"), component.MaxDescriptionLen),
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

func (w *ContainerStack) Decode(outputs auto.OutputMap) (provider.StackResult, error) {
	containers := make([]provider.AppContainer, 0, len(w.workers))
	for _, worker := range w.workers {
		logical := resources.WorkerName(worker.Name)
		fields, err := component.RequireOutputFields(outputs, logical)
		if err != nil {
			return provider.StackResult{}, err
		}
		physical, err := component.RequireStringField(fields, logical, container.OutputKeyPhysical)
		if err != nil {
			return provider.StackResult{}, err
		}
		containers = append(containers, provider.AppContainer{Name: logical, Physical: physical, Image: w.web.Image})
	}
	return provider.StackResult{Containers: containers}, nil
}
