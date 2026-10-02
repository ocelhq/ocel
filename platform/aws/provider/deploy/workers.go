package deploy

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"time"

	iam "github.com/pulumi/pulumi-aws/sdk/v7/go/aws/iam"
	lambda "github.com/pulumi/pulumi-aws/sdk/v7/go/aws/lambda"
	scheduler "github.com/pulumi/pulumi-aws/sdk/v7/go/aws/scheduler"
	"github.com/pulumi/pulumi/sdk/v3/go/auto"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/processenv"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	"github.com/ocelhq/ocel/platform/aws/provider/queues"
)

const (
	workersKey = "workers"

	schedulerServicePrincipal = "scheduler.amazonaws.com"

	reportBatchItemFailures = "ReportBatchItemFailures"
	minEventConcurrency     = 2
	maxEventConcurrency     = 1000
	maxUnbatchedSize        = 10

	cronInput = `{"ocelCron":%q,"scheduledTime":"<aws.scheduler.scheduled-time>"}`
)

type workersWork struct {
	project  string
	stack    naming.StackName
	topics   []deployedTopic
	workers  []provider.WorkerSpec
	args     functionArgs
	artifact artifactRef
	env      map[string]string
	role     executionRole
	layer    string
	kms      string
	region   string
	account  string
	table    string
	prefix   string
	group    string
	outputs  auto.OutputMap
}

func workersStack(env, app string) naming.StackName {
	return naming.StackName{Env: env, App: app}
}

func workersAt(ref provider.StackRef) keyvalue.Key {
	return stackrecords.StacksPartition(ref.Tier, ref.Project).Key(workersKey, ref.Name.Env, ref.Name.App)
}

func (r *release) workersWork(spec provider.StackSpec, work *appWork) (*workersWork, error) {
	app := spec.App
	project := naming.Sanitize(spec.Ref.Project)
	if len(work.functions.Functions) == 0 {
		return nil, fmt.Errorf("app %s hosts workers %v and ships no function to run them from", app.App, workerNames(app.Workers))
	}
	entry := work.functions.Functions[0]
	args := work.functions.Args(entry)
	return &workersWork{
		project:  project,
		stack:    workersStack(spec.Ref.Name.Env, app.App),
		topics:   topicsOf(project, spec.Ref.Name.Env, app.Topics),
		workers:  app.Workers,
		args:     args,
		artifact: work.functions.Artifacts[entry.Logical],
		env:      work.functions.Env,
		role:     work.role,
		layer:    work.functions.Layers[args.Arch],
		kms:      work.functions.KmsKeyARN,
		region:   r.cfg.Region,
		account:  accountOfARN(r.cfg.StateTableARN),
		table:    r.cfg.StateTableARN,
		prefix:   naming.TaskKeyPrefix(project, spec.Ref.Name.Env),
		group:    queues.ScheduleGroupName(project, spec.Ref.Name.Env),
	}, nil
}

func workerNames(workers []provider.WorkerSpec) []string {
	names := make([]string, 0, len(workers))
	for _, worker := range workers {
		names = append(names, worker.Name)
	}
	return names
}

func (r *release) provisionWorkers(ctx context.Context, spec provider.StackSpec, work *appWork, progress progress.Log) error {
	if spec.App == nil || len(spec.App.Workers) == 0 || work == nil {
		return nil
	}
	if spec.Ref.Name.Env == "" {
		return nil
	}
	hosted, err := r.workersWork(spec, work)
	if err != nil {
		return err
	}
	ref := provider.StackRef{Project: spec.Ref.Project, Tier: spec.Ref.Tier, Name: hosted.stack}
	if r.cfg.KeyValues != nil {
		entry, err := keyvalue.ReadOrEmpty(ctx, r.cfg.KeyValues, workersAt(ref))
		if err != nil {
			return err
		}
		entry.Value = []byte("{}")
		if _, err := r.cfg.KeyValues.Write(ctx, entry); err != nil {
			return fmt.Errorf("record the workers of %s: %w", spec.App.App, err)
		}
	}
	_, err = r.automation.Run(ctx, provider.StackSpec{Ref: ref, Kind: provider.StackApp, Tags: spec.Tags, VendorState: hosted}, progress)
	if err != nil {
		return fmt.Errorf("provision the workers of %s: %w", spec.App.App, err)
	}
	return nil
}

func (r *Stacks) destroyWorkers(ctx context.Context, opened *release, ref provider.StackRef, progress progress.Log) error {
	store := opened.cfg.KeyValues
	if store == nil || !ref.Name.IsInfra() {
		return nil
	}
	partition := stackrecords.StacksPartition(ref.Tier, ref.Project)
	recorded, err := store.List(ctx, partition, workersKey, ref.Name.Env)
	if err != nil {
		return err
	}
	for _, entry := range recorded {
		if len(entry.Key.Path) != 3 {
			continue
		}
		stack := provider.StackRef{Project: ref.Project, Tier: ref.Tier, Name: workersStack(entry.Key.Path[1], entry.Key.Path[2])}
		if err := opened.automation.Destroy(ctx, stack, progress); err != nil {
			return fmt.Errorf("take down the workers of %s: %w", stack.Name.App, err)
		}
		if err := keyvalue.Forget(ctx, store, entry.Key); err != nil {
			return err
		}
	}
	return nil
}

func (w *workersWork) served(worker string) []consumerQueue {
	var served []consumerQueue
	for _, topic := range w.topics {
		for _, queue := range topic.queues {
			if queue.consumer.Worker == worker {
				served = append(served, queue)
			}
		}
	}
	return served
}

func (w *workersWork) cronTasks(worker string) []deployedTopic {
	var tasks []deployedTopic
	for _, topic := range w.topics {
		if topic.isTask() && topic.declared.Cron != "" && topic.queues[0].consumer.Worker == worker {
			tasks = append(tasks, topic)
		}
	}
	return tasks
}

func (w *workersWork) coordinate(kind naming.Kind, name string) naming.Coordinate {
	return naming.Coordinate{Project: w.project, Env: w.stack.Env, App: w.stack.App, Kind: kind, Name: name}
}

func (w *workersWork) run(ctx *pulumi.Context) error {
	role, err := newFunctionRole(ctx, w.coordinate(naming.KindRole, "worker-role"), w.role)
	if err != nil {
		return err
	}
	consumption, err := w.consumptionPolicy()
	if err != nil {
		return err
	}
	consuming, err := iam.NewRolePolicy(ctx, naming.ResourceID(naming.KindRole, roleLocalName, "policy", "queues"), &iam.RolePolicyArgs{
		Role:   role.Name,
		Policy: pulumi.String(consumption),
	})
	if err != nil {
		return err
	}
	var functions []*lambda.Function
	var crons []cronTarget
	for _, worker := range w.workers {
		fn, err := w.declareWorker(ctx, worker, role.Arn, consuming)
		if err != nil {
			return err
		}
		functions = append(functions, fn)
		for _, task := range w.cronTasks(worker.Name) {
			crons = append(crons, cronTarget{task: task, function: fn})
		}
	}
	if len(crons) > 0 {
		return w.declareCrons(ctx, functions, crons)
	}
	return nil
}

func (w *workersWork) consumptionPolicy() (string, error) {
	var queueARNs []string
	for _, worker := range w.workers {
		for _, queue := range w.served(worker.Name) {
			queueARNs = append(queueARNs, sqsQueueARN(w.region, w.account, queue.name))
		}
	}
	statements := []any{map[string]any{
		"Effect":   "Allow",
		"Action":   []string{"dynamodb:DeleteItem", "dynamodb:GetItem", "dynamodb:PutItem", "dynamodb:Query", "dynamodb:UpdateItem"},
		"Resource": w.table,
		"Condition": map[string]any{
			"ForAllValues:StringLike": map[string]any{"dynamodb:LeadingKeys": []string{w.prefix + "*"}},
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

func (w *workersWork) declareWorker(ctx *pulumi.Context, worker provider.WorkerSpec, roleARN pulumi.StringInput, consuming pulumi.Resource) (*lambda.Function, error) {
	at := w.coordinate(naming.KindWorker, worker.Name)
	name := at.PhysicalName(maxLambdaNameLen)
	env := pulumi.StringMap{}
	args := w.args
	args.TimeoutSeconds = int(workerTimeout(w.topics, worker.Name) / time.Second)
	for key, value := range functionEnv(w.env, args, nil, nil) {
		env[key] = pulumi.String(value)
	}
	env[processenv.WorkerEnvVar] = pulumi.String(worker.Name)
	if worker.Concurrency > 0 {
		env[queues.WorkerConcurrencyEnv] = pulumi.String(strconv.Itoa(worker.Concurrency))
	}
	logs, err := newLambdaLogGroup(ctx, naming.ResourceID(naming.KindWorker, worker.Name, "logs"), name, at.Kind, "", args.Tags)
	if err != nil {
		return nil, err
	}
	fn, err := lambda.NewFunction(ctx, naming.ResourceID(naming.KindWorker, worker.Name), &lambda.FunctionArgs{
		Name:          pulumi.String(name),
		Description:   capDescription(at.Description("worker "+worker.Name+": serves its tasks and consumers from their queues"), maxDescriptionLen),
		Runtime:       pulumi.String(args.Runtime),
		Handler:       pulumi.String(lambdaHandler(args)),
		Role:          roleARN,
		S3Bucket:      pulumi.String(w.artifact.Bucket),
		S3Key:         pulumi.String(w.artifact.Key),
		MemorySize:    pulumi.Int(args.MemorySizeMB),
		Timeout:       pulumi.Int(args.TimeoutSeconds),
		Environment:   &lambda.FunctionEnvironmentArgs{Variables: env},
		KmsKeyArn:     optionalARN(w.kms),
		LoggingConfig: lambdaLogging(logs),
		Tags:          resourceTags(at.Kind, "", args.Tags),
		Architectures: pulumi.StringArray{pulumi.String(args.Arch)},
		Layers:        pulumi.StringArray{pulumi.String(w.layer)},
	})
	if err != nil {
		return nil, err
	}
	for _, queue := range w.served(worker.Name) {
		if _, err := lambda.NewEventSourceMapping(ctx, naming.ResourceID(naming.KindWorker, worker.Name, queue.topic, queue.consumer.Name),
			eventSourceMapping(queue, worker, fn, w.region, w.account, resourceTags(at.Kind, "", args.Tags)), pulumi.DependsOn([]pulumi.Resource{consuming})); err != nil {
			return nil, err
		}
	}
	return fn, nil
}

func eventSourceMapping(queue consumerQueue, worker provider.WorkerSpec, fn *lambda.Function, region, account string, tags pulumi.StringMap) *lambda.EventSourceMappingArgs {
	args := &lambda.EventSourceMappingArgs{
		EventSourceArn:        pulumi.String(sqsQueueARN(region, account, queue.name)),
		FunctionName:          fn.Arn,
		BatchSize:             pulumi.Int(1),
		FunctionResponseTypes: pulumi.StringArray{pulumi.String(reportBatchItemFailures)},
		Tags:                  tags,
	}
	if batch := queue.consumer.Batch; batch != nil && batch.Size > 0 {
		args.BatchSize = pulumi.Int(batch.Size)
		if !queue.fifo {
			window := int(batch.Timeout / time.Second)
			if batch.Size > maxUnbatchedSize {
				window = max(window, 1)
			}
			if window > 0 {
				args.MaximumBatchingWindowInSeconds = pulumi.Int(window)
			}
		}
	}
	if concurrency := eventConcurrency(queue.consumer.Concurrency, worker.Concurrency); concurrency > 0 {
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

type cronTarget struct {
	task     deployedTopic
	function *lambda.Function
}

func (w *workersWork) declareCrons(ctx *pulumi.Context, functions []*lambda.Function, crons []cronTarget) error {
	at := w.coordinate(naming.KindRole, "scheduler")
	role, err := iam.NewRole(ctx, naming.ResourceID(naming.KindRole, "scheduler"), &iam.RoleArgs{
		NamePrefix:          pulumi.String(rolePrefix(at)),
		Description:         capDescription(at.Description("role EventBridge Scheduler takes to start this app's cron tasks on their workers"), maxDescriptionLen),
		AssumeRolePolicy:    pulumi.String(assumeRolePolicy(schedulerServicePrincipal)),
		PermissionsBoundary: pulumi.String(w.role.Boundary),
		Tags:                resourceTags(at.Kind, "", w.role.Tags),
	})
	if err != nil {
		return err
	}
	arns := make([]any, 0, len(functions))
	for _, fn := range functions {
		arns = append(arns, fn.Arn)
	}
	policy := pulumi.All(arns...).ApplyT(func(resolved []any) (string, error) {
		targets := make([]string, 0, len(resolved))
		for _, arn := range resolved {
			targets = append(targets, fmt.Sprint(arn))
		}
		encoded, err := json.Marshal(map[string]any{"Version": "2012-10-17", "Statement": []any{map[string]any{
			"Effect": "Allow", "Action": "lambda:InvokeFunction", "Resource": targets,
		}}})
		return string(encoded), err
	}).(pulumi.StringOutput)
	invoking, err := iam.NewRolePolicy(ctx, naming.ResourceID(naming.KindRole, "scheduler", "invoke"), &iam.RolePolicyArgs{Role: role.Name, Policy: policy})
	if err != nil {
		return err
	}
	for _, cron := range crons {
		task := cron.task.resource.Declared
		expressions, err := schedulerExpressions(cron.task.declared.Cron)
		if err != nil {
			return fmt.Errorf("task %s: %w", task, err)
		}
		for i, expression := range expressions {
			name := queues.CronScheduleName(task)
			if len(expressions) > 1 {
				name = queues.CronScheduleName(task + "-" + strconv.Itoa(i+1))
			}
			if _, err := scheduler.NewSchedule(ctx, naming.ResourceID(naming.KindWorker, "cron", name), &scheduler.ScheduleArgs{
				Name:               pulumi.String(name),
				GroupName:          pulumi.String(w.group),
				ScheduleExpression: pulumi.String(expression),
				FlexibleTimeWindow: &scheduler.ScheduleFlexibleTimeWindowArgs{Mode: pulumi.String("OFF")},
				Target: &scheduler.ScheduleTargetArgs{
					Arn:     cron.function.Arn,
					RoleArn: role.Arn,
					Input:   pulumi.String(fmt.Sprintf(cronInput, task)),
				},
			}, pulumi.DependsOn([]pulumi.Resource{invoking})); err != nil {
				return err
			}
		}
	}
	return nil
}

func scheduleGroupNeeded(topics []deployedTopic) bool {
	return slices.ContainsFunc(topics, func(topic deployedTopic) bool { return topic.isTask() && topic.declared.Cron != "" })
}
