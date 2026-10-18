package worker

import (
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/platform/aws/provider/component"
	"github.com/ocelhq/ocel/platform/aws/provider/component/componenttest"
	"github.com/ocelhq/ocel/platform/aws/provider/compute/container"
	"github.com/ocelhq/ocel/platform/aws/provider/resources/task"
	"github.com/ocelhq/ocel/platform/aws/provider/resources/topic"
	"github.com/ocelhq/ocel/platform/aws/provider/resources/variables"
	"github.com/ocelhq/ocel/platform/aws/provider/tier/containers"
	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

const (
	testStateTableARN = "arn:aws:dynamodb:us-east-1:" + mockAccount + ":table/ocel-state"
	containerImage    = "123456789012.dkr.ecr.us-east-1.amazonaws.com/ocel/web:sha256-0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	fixtureSecret     = "5e884898da28047151d0e56f8dc6292773603d0d"
)

func fixedRelease(t *testing.T) naming.ReleaseToken {
	t.Helper()
	release, err := naming.ParseReleaseToken("r3f8a1c90")
	if err != nil {
		t.Fatalf("parse release: %v", err)
	}
	return release
}

func taskResource(name string, consumer provider.ConsumerSpec, options ...func(*provider.TopicSpec)) provider.Resource {
	consumer.Name, consumer.Exclusive = name, true
	topic := &provider.TopicSpec{Consumers: []provider.ConsumerSpec{consumer}}
	for _, option := range options {
		option(topic)
	}
	return provider.Resource{Name: "topic--" + name, Declared: name, Type: provider.BindingTask, Topic: topic}
}

func topicResource(name string, consumers ...provider.ConsumerSpec) provider.Resource {
	return provider.Resource{Name: "topic--" + name, Declared: name, Type: provider.BindingTopic, Topic: &provider.TopicSpec{Consumers: consumers}}
}

func orderedTopic(topic *provider.TopicSpec) { topic.Ordered = true }

func cronEveryMinute(topic *provider.TopicSpec) { topic.Cron = "* * * * *" }

func fixtureContainerInfra() containers.Infrastructure {
	return containers.Infrastructure{
		VPC:           "vpc-123",
		Subnets:       []string{"subnet-a", "subnet-b"},
		Listener:      "arn:aws:elasticloadbalancing:us-east-1:123456789012:listener/app/ocel-containers-production/abc/def",
		OriginHost:    "internal-ocel-containers-production-123.us-east-1.elb.amazonaws.com",
		VPCOrigin:     "vo_2XyZ3abc4DEF5ghi",
		TaskSecurity:  "sg-tasks",
		Cluster:       "arn:aws:ecs:us-east-1:123456789012:cluster/ocel-containers-production",
		ExecutionRole: "arn:aws:iam::123456789012:role/ocel-containers-production-exec-abc",
		LogGroup:      "/ocel/containers/production",
	}
}

func containerStackSpec(t *testing.T) (container.Target, provider.StackSpec) {
	t.Helper()
	at := container.Target{
		Region:            "us-east-1",
		AppBoundaryARN:    "arn:aws:iam::123456789012:policy/ocel-app-boundary",
		AppRolePath:       componenttest.AppRolePath,
		OriginSecret:      fixtureSecret,
		Slug:              "shop",
		Tier:              environment.TierProduction,
		StateTable:        topic.StateTable{Region: "us-east-1"},
		Variables:         variables.Store{Table: "ocel-variables", KeyARN: "arn:aws:kms:us-east-1:123456789012:key/abcd", Tier: environment.TierProduction},
		VariablesTableARN: "arn:aws:dynamodb:us-east-1:123456789012:table/ocel-variables",
	}
	stack := naming.AppStack("prod", "web", fixedRelease(t))
	spec := provider.StackSpec{
		Ref:      provider.StackRef{Project: "shop", Tier: environment.TierProduction, Name: stack},
		Kind:     provider.StackApp,
		Recorded: true,
		Tags:     map[string]string{"ocel:managed-by": "ocel"},
		Images:   provider.ImagePushes{Store: fake.NewImages(), Pushes: []provider.ImagePush{{App: "web", ImageRef: containerImage}}},
		App: &provider.AppSpec{
			App:             "web",
			BuildID:         "d1",
			Compute:         provider.ComputeContainer,
			Image:           containerImage,
			HealthCheckPath: "/healthz",
			Instances:       provider.Instances{Min: 1, Max: 1},
			Values: provider.AppValues{
				Plain:     map[string]string{"GREETING": "hello"},
				Sensitive: map[string]string{"API_TOKEN": "sensitive-token"},
				Phase:     "production",
			},
		},
	}
	return at, spec
}

func definitionOf(at container.Target, spec provider.StackSpec) (*container.Definition, error) {
	work, err := container.NewDefinition(spec, at)
	if err != nil {
		return nil, err
	}
	work.Infra = fixtureContainerInfra()
	return work, nil
}

func targetOf(at container.Target) Target {
	return Target{Tier: at.Tier, StateTable: at.StateTable}
}

func hostingContainerWorkers(t *testing.T, workers ...provider.WorkerSpec) (container.Target, provider.StackSpec, *ContainerStack) {
	t.Helper()
	return hostingContainerWorkersBeside(t, nil, workers...)
}

func hostingContainerWorkersBeside(t *testing.T, more []provider.Resource, workers ...provider.WorkerSpec) (container.Target, provider.StackSpec, *ContainerStack) {
	t.Helper()
	at, spec := containerStackSpec(t)
	at.StateTable.Name, at.StateTable.ARN = "ocel-state", testStateTableARN
	spec.Ref.Name = naming.AppStack("prod", "web", fixedRelease(t))
	spec.App.Instances = provider.Instances{Min: 1, Max: 3}
	spec.App.Workers = workers
	spec.App.DeploymentWorkers = workers
	spec.App.Topics = []provider.Resource{
		taskResource("resize", provider.ConsumerSpec{Worker: "media", MaxDuration: time.Hour}),
		taskResource("thumbnail", provider.ConsumerSpec{Worker: "media"}),
		topicResource("orders", provider.ConsumerSpec{Name: "audit-log", Worker: "worker"}),
	}
	spec.App.Topics = append(spec.App.Topics, more...)
	work, err := definitionOf(at, spec)
	if err != nil {
		t.Fatalf("containerWork() = %v", err)
	}
	hosted, err := NewContainerStack(spec, work, targetOf(at))
	if err != nil {
		t.Fatalf("containerWorkersWork() = %v", err)
	}
	return at, spec, hosted
}

func programRecorded(t *testing.T, hosted *ContainerStack) *inputRecorder {
	t.Helper()
	rec := &inputRecorder{}
	if err := pulumi.RunErr(func(pctx *pulumi.Context) error { return hosted.Declare(pctx) }, pulumi.WithMocks("shop", "prod--web", rec)); err != nil {
		t.Fatalf("run the container workers program: %v", err)
	}
	return rec
}

var mediaOnContainers = provider.WorkerSpec{Name: "media", Concurrency: 2, App: "web", Compute: provider.ComputeContainer}

func recordedOf(t *testing.T, rec *inputRecorder, typeToken string) resource.PropertyMap {
	t.Helper()
	for key, inputs := range rec.recorded {
		if strings.HasPrefix(key, typeToken+"::") {
			return inputs
		}
	}
	t.Fatalf("the program declared no %s among %d resources", typeToken, len(rec.recorded))
	return nil
}

func declareTopicsAndTasks(pctx *pulumi.Context, region, stateTableARN, project, env string, resources []provider.Resource) error {
	at := component.Stack{Project: project, Env: env, Region: region, StateTableARN: stateTableARN, Resources: resources}
	for _, declaring := range []component.Component{task.New(), topic.New()} {
		if err := declaring.Declare(pctx, at, declaring.Declared(resources)); err != nil {
			return err
		}
	}
	return nil
}
