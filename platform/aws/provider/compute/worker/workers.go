package worker

import (
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"time"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/processenv"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/resources"
	"github.com/ocelhq/ocel/platform/aws/provider/component"
	"github.com/ocelhq/ocel/platform/aws/provider/compute/function"
	"github.com/ocelhq/ocel/platform/aws/provider/queues"
	"github.com/ocelhq/ocel/platform/aws/provider/resources/task"
	"github.com/ocelhq/ocel/platform/aws/provider/resources/topic"
	"github.com/ocelhq/ocel/platform/aws/provider/tier/functionnetwork"
	lambda "github.com/pulumi/pulumi-aws/sdk/v7/go/aws/lambda"
	"github.com/pulumi/pulumi/sdk/v3/go/auto"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

const (
	workerRoleWord    = "worker-role"
	schedulerRoleWord = "scheduler-role"
	queuesPolicyName  = "queues"

	reportBatchItemFailures = "ReportBatchItemFailures"
	minEventConcurrency     = 2
	maxEventConcurrency     = 1000
)

type Target struct {
	Namespace  string
	Tier       environment.Tier
	StateTable topic.StateTable
}

type Stack struct {
	names    queues.Names
	project  string
	stack    naming.StackName
	topics   topic.Topics
	workers  []provider.WorkerSpec
	args     function.Args
	artifact function.Artifact
	env      map[string]string
	role     function.ExecutionRole
	network  *functionnetwork.Network
	layer    string
	kms      string
	region   string
	account  string
	table    string
	prefix   string
	group    string
}

func StackName(env, app string) naming.StackName {
	return naming.StackName{Env: env, App: app}
}

func NewStack(spec provider.StackSpec, functions function.AppStack, role function.ExecutionRole, network *functionnetwork.Network, target Target) (*Stack, error) {
	app := spec.App
	project := naming.Sanitize(spec.Ref.Project)
	at := slices.IndexFunc(functions.Functions, func(fn function.AppFunction) bool { return fn.HasWorkerEntry })
	if at < 0 {
		return nil, fmt.Errorf("app %s hosts workers %v and ships no function carrying a worker entry to start them from", app.App, workerNames(app.Workers))
	}
	entry := functions.Functions[at]
	args := functions.Args(entry)
	names := queues.Names{Namespace: target.Namespace, Project: project, Env: spec.Ref.Name.Env}
	topics, err := topic.Of(names, app.Topics)
	if err != nil {
		return nil, err
	}
	group, err := names.ScheduleGroup()
	if err != nil {
		return nil, err
	}
	return &Stack{
		names:    names,
		project:  project,
		stack:    StackName(spec.Ref.Name.Env, app.App),
		topics:   topics,
		workers:  app.Workers,
		args:     args,
		artifact: functions.Artifacts[entry.Logical],
		env:      functions.Env,
		role:     role,
		network:  network,
		layer:    functions.Layers[args.Arch],
		kms:      functions.KmsKeyARN,
		region:   target.StateTable.Region,
		account:  component.AccountOfARN(target.StateTable.ARN),
		table:    target.StateTable.ARN,
		prefix:   naming.TaskKeyPrefix(project, spec.Ref.Name.Env),
		group:    group,
	}, nil
}

func workerNames(workers []provider.WorkerSpec) []string {
	names := make([]string, 0, len(workers))
	for _, worker := range workers {
		names = append(names, worker.Name)
	}
	return names
}

func (w *Stack) Decode(outputs auto.OutputMap) (provider.StackResult, error) {
	functions := make([]provider.Function, 0, len(w.workers))
	for _, worker := range w.workers {
		logical := resources.WorkerName(worker.Name)
		fields, err := component.RequireOutputFields(outputs, logical)
		if err != nil {
			return provider.StackResult{}, err
		}
		physical, err := component.RequireStringField(fields, logical, function.OutputKeyName)
		if err != nil {
			return provider.StackResult{}, err
		}
		functions = append(functions, provider.Function{Name: logical, Physical: physical})
	}
	return provider.StackResult{Functions: functions}, nil
}

func (w *Stack) coordinate(kind naming.Kind, name string) naming.Coordinate {
	return naming.Coordinate{Project: w.project, Env: w.stack.Env, App: w.stack.App, Kind: kind, Name: name}
}

func (w *Stack) Declare(ctx *pulumi.Context) error {
	roleAt := w.coordinate(naming.KindRole, workerRoleWord)
	role, err := function.NewRole(ctx, roleAt, w.role)
	if err != nil {
		return err
	}
	roleName, err := function.RoleName(w.names.Namespace, roleAt)
	if err != nil {
		return err
	}
	consumption, err := w.consumptionPolicy()
	if err != nil {
		return err
	}
	consuming, err := component.NewRolePolicy(ctx, roleName, role, queuesPolicyName, pulumi.String(consumption))
	if err != nil {
		return err
	}
	var functions []*lambda.Function
	var crons []task.CronTarget
	for _, worker := range w.workers {
		fn, err := w.declareWorker(ctx, worker, role.Arn, consuming)
		if err != nil {
			return err
		}
		functions = append(functions, fn)
		ctx.Export(resources.WorkerName(worker.Name), pulumi.Map{function.OutputKeyName: fn.Name})
		for _, cronTask := range w.topics.CronTasks(worker.Name) {
			crons = append(crons, task.CronTarget{Task: cronTask, ARN: fn.Arn})
		}
	}
	if len(crons) == 0 {
		return nil
	}
	schedulerAt := w.coordinate(naming.KindRole, schedulerRoleWord)
	scheduler, err := component.PhysicalName(w.names.Namespace, schedulerAt, component.MaxRoleNameLen)
	if err != nil {
		return err
	}
	assumed := task.SchedulerRole{Name: scheduler, At: schedulerAt, Boundary: w.role.Boundary, Path: w.role.Path, Tags: w.role.Tags}
	return task.DeclareCrons(ctx, assumed, w.names, w.group, invokingPolicy(functions), crons)
}

func invokingPolicy(functions []*lambda.Function) pulumi.StringOutput {
	arns := make([]any, 0, len(functions))
	for _, fn := range functions {
		arns = append(arns, fn.Arn)
	}
	return pulumi.All(arns...).ApplyT(func(resolved []any) (string, error) {
		targets := make([]string, 0, len(resolved))
		for _, arn := range resolved {
			targets = append(targets, fmt.Sprint(arn))
		}
		return task.Allowing("lambda:InvokeFunction", targets)
	}).(pulumi.StringOutput)
}

func (w *Stack) consumptionPolicy() (string, error) {
	return consumptionPolicy(w.workers, w.topics, w.region, w.account, w.table, w.prefix)
}

func consumptionPolicy(workers []provider.WorkerSpec, topics topic.Topics, region, account, table, prefix string) (string, error) {
	var queueARNs []string
	for _, worker := range workers {
		for _, queue := range topics.Served(worker.Name) {
			queueARNs = append(queueARNs, topic.SQSQueueARN(region, account, queue.Name))
		}
	}
	statements := []any{map[string]any{
		"Effect":   "Allow",
		"Action":   []string{"dynamodb:DeleteItem", "dynamodb:GetItem", "dynamodb:PutItem", "dynamodb:Query", "dynamodb:UpdateItem"},
		"Resource": table,
		"Condition": map[string]any{
			"ForAllValues:StringLike": map[string]any{"dynamodb:LeadingKeys": []string{prefix + "*"}},
		},
	}}
	if len(queueARNs) > 0 {
		statements = append(statements, map[string]any{
			"Effect":   "Allow",
			"Action":   []string{"sqs:ChangeMessageVisibility", "sqs:DeleteMessage", "sqs:GetQueueAttributes", "sqs:GetQueueUrl", "sqs:ReceiveMessage", "sqs:SendMessage"},
			"Resource": queueARNs,
		})
	}
	encoded, err := json.Marshal(map[string]any{"Version": "2012-10-17", "Statement": statements})
	return string(encoded), err
}

func (w *Stack) declareWorker(ctx *pulumi.Context, worker provider.WorkerSpec, roleARN pulumi.StringInput, consuming pulumi.Resource) (*lambda.Function, error) {
	at := w.coordinate(naming.KindWorker, worker.Name)
	physical, err := component.PhysicalName(w.names.Namespace, at, component.MaxLambdaNameLen)
	if err != nil {
		return nil, err
	}
	name := component.NameOf(physical)
	env := pulumi.StringMap{}
	args := w.args
	args.TimeoutSeconds = int(topic.WorkerTimeout(w.topics, worker.Name) / time.Second)
	args.Network = w.network
	for key, value := range function.Env(w.env, args, nil, nil) {
		env[key] = pulumi.String(value)
	}
	env[processenv.WorkerEnvVar] = pulumi.String(worker.Name)
	if worker.Concurrency > 0 {
		env[queues.WorkerConcurrencyEnv] = pulumi.String(strconv.Itoa(worker.Concurrency))
	}
	logs, err := component.NewLambdaLogGroup(ctx, name, at.Kind, "", args.Tags)
	if err != nil {
		return nil, err
	}
	fn, err := lambda.NewFunction(ctx, name.Pulumi, &lambda.FunctionArgs{
		Name:          name.AWS,
		Description:   component.CapDescription(at.Description("worker "+worker.Name+": serves its tasks and consumers from their queues"), component.MaxDescriptionLen),
		Runtime:       pulumi.String(args.Runtime),
		Handler:       pulumi.String(args.LambdaHandler()),
		Role:          roleARN,
		S3Bucket:      pulumi.String(w.artifact.Bucket),
		S3Key:         pulumi.String(w.artifact.Key),
		MemorySize:    pulumi.Int(args.MemorySizeMB),
		Timeout:       pulumi.Int(args.TimeoutSeconds),
		Environment:   &lambda.FunctionEnvironmentArgs{Variables: env},
		KmsKeyArn:     component.OptionalARN(w.kms),
		LoggingConfig: component.LambdaLogging(logs),
		Tags:          component.Tags(at.Kind, "", args.Tags),
		Architectures: pulumi.StringArray{pulumi.String(args.Arch)},
		Layers:        function.RuntimeLayerInput(w.layer),
		VpcConfig:     args.Network.VPCConfig(),
	})
	if err != nil {
		return nil, err
	}
	for _, queue := range w.topics.Served(worker.Name) {
		if _, err := lambda.NewEventSourceMapping(ctx, name.Part(queue.Name),
			eventSourceMapping(queue, worker, fn, w.region, w.account, component.Tags(at.Kind, "", args.Tags)), pulumi.DependsOn([]pulumi.Resource{consuming})); err != nil {
			return nil, err
		}
	}
	return fn, nil
}

func eventSourceMapping(queue topic.Queue, worker provider.WorkerSpec, fn *lambda.Function, region, account string, tags pulumi.StringMap) *lambda.EventSourceMappingArgs {
	args := &lambda.EventSourceMappingArgs{
		EventSourceArn:        pulumi.String(topic.SQSQueueARN(region, account, queue.Name)),
		FunctionName:          fn.Arn,
		BatchSize:             pulumi.Int(1),
		FunctionResponseTypes: pulumi.StringArray{pulumi.String(reportBatchItemFailures)},
		Tags:                  tags,
	}
	if batch := queue.Consumer.Batch; batch != nil && batch.Size > 0 {
		args.BatchSize = pulumi.Int(batch.Size)
	}
	if window := topic.BatchingWindowSeconds(queue); window > 0 {
		args.MaximumBatchingWindowInSeconds = pulumi.Int(window)
	}
	if concurrency := eventConcurrency(queue.Consumer.Concurrency, worker.Concurrency); concurrency > 0 {
		args.ScalingConfig = &lambda.EventSourceMappingScalingConfigArgs{MaximumConcurrency: pulumi.Int(concurrency)}
	}
	return args
}

func eventConcurrency(consumer, worker int) int {
	limit := consumer
	if worker > 0 && (limit == 0 || worker < limit) {
		limit = worker
	}
	if limit == 0 {
		return 0
	}
	return min(max(limit, minEventConcurrency), maxEventConcurrency)
}
