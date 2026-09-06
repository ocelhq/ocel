package deploy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"maps"
	"slices"
	"strings"

	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/ecs"
	iam "github.com/pulumi/pulumi-aws/sdk/v7/go/aws/iam"
	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/lb"
	"github.com/pulumi/pulumi/sdk/v3/go/auto"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/providerkit"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const (
	containerPort       = "8080"
	containerPortNumber = 8080
	containerCPU        = "512"
	containerMemory     = "1024"
	containerName       = "app"
	containerPortEnv    = "PORT"

	containerLocalName = "container"

	targetGroupNamePrefix = "ocel-"
	deregistrationSeconds = 10
	healthGraceSeconds    = 60

	healthCheckIntervalSeconds = 15
	healthCheckTimeoutSeconds  = 5
	healthyThreshold           = 2
	unhealthyThreshold         = 3
	healthyStatuses            = "200-399"

	outputKeyContainerURL      = "url"
	outputKeyContainerPhysical = "physical"

	maxRulePriority = 50000
)

type containerWork struct {
	app        string
	image      string
	healthPath string
	env        map[string]string
	tags       map[string]string
	boundary   string
	region     string
	secret     string
	policies   []linkPolicy
	service    naming.Coordinate
	role       naming.Coordinate
	substrate  substrate
}

type containerDefinition struct {
	Name         string            `json:"name"`
	Image        string            `json:"image"`
	Essential    bool              `json:"essential"`
	PortMappings []portMapping     `json:"portMappings"`
	Environment  []environmentPair `json:"environment"`
	LogConfig    logConfiguration  `json:"logConfiguration"`
}

type portMapping struct {
	ContainerPort int    `json:"containerPort"`
	Protocol      string `json:"protocol"`
}

type environmentPair struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type logConfiguration struct {
	Driver  string            `json:"logDriver"`
	Options map[string]string `json:"options"`
}

func (r *release) checkContainer(plan providerkit.StackPlan) (*containerWork, error) {
	app := plan.App
	project, stack := naming.Sanitize(plan.Ref.Project), plan.Ref.Name
	if strings.TrimSpace(app.Image) == "" {
		return nil, providerkit.Refuse(providerkit.CodeInvalid,
			"app %s names no image, and a container on this provider runs what a registry coordinate names and nothing else", app.App)
	}
	if !providerkit.HealthCheckPath(app.HealthCheckPath) {
		return nil, providerkit.Refuse(providerkit.CodeInvalid,
			"app %s is probed at %q, which is not a path a load balancer can send a health check to", app.App, app.HealthCheckPath)
	}
	if r.cfg.OriginSecret == "" {
		return nil, providerkit.Refuse(providerkit.CodeNotReady,
			"this class holds no origin secret, and a container answers only to an edge that presents one: re-run `%s`", providerkit.BootstrapCommand(plan.Ref.Class))
	}
	policies, err := planLinkPolicies(app.Grants)
	if err != nil {
		return nil, err
	}
	env, err := containerEnv(app.App, app.Values)
	if err != nil {
		return nil, err
	}
	return &containerWork{
		app:        app.App,
		image:      app.Image,
		healthPath: app.HealthCheckPath,
		env:        env,
		tags:       plan.Tags,
		boundary:   r.cfg.AppBoundaryARN,
		region:     r.cfg.Region,
		secret:     r.cfg.OriginSecret,
		policies:   policies,
		service:    serviceCoordinate(project, stack),
		role:       roleCoordinate(project, stack),
	}, nil
}

func (r *release) containerWork(plan providerkit.StackPlan, held substrate) (*containerWork, error) {
	work, err := r.checkContainer(plan)
	if err != nil {
		return nil, err
	}
	work.substrate = held
	return work, nil
}

func rulePriority(physical string) int {
	sum := fnv.New32a()
	sum.Write([]byte(physical))
	return 1 + int(sum.Sum32()%uint32(maxRulePriority))
}

func containerEnv(app string, values providerkit.AppValues) (map[string]string, error) {
	env := make(map[string]string, len(values.Delivered)+2)
	maps.Copy(env, values.Delivered)
	maps.Copy(env, values.Injected())
	if _, set := env[containerPortEnv]; set {
		return nil, providerkit.Refuse(providerkit.CodeInvalid,
			"app %s sets %s, and a container on this provider listens on %s, which it is handed as %s: drop the value and read the port from the environment",
			app, containerPortEnv, containerPort, containerPortEnv)
	}
	env[containerPortEnv] = containerPort
	return env, nil
}

func serviceCoordinate(project string, stack naming.StackName) naming.Coordinate {
	return naming.Coordinate{
		Project: project,
		Env:     stack.Env,
		App:     stack.App,
		Kind:    naming.KindService,
		Name:    containerLocalName,
		Release: stack.Release,
	}
}

func (w *containerWork) physical() string {
	return w.service.PhysicalName(maxLambdaNameLen)
}

func (w *containerWork) definition() (string, error) {
	pairs := make([]environmentPair, 0, len(w.env))
	for _, key := range slices.Sorted(maps.Keys(w.env)) {
		pairs = append(pairs, environmentPair{Name: key, Value: w.env[key]})
	}
	encoded, err := json.Marshal([]containerDefinition{{
		Name:         containerName,
		Image:        w.image,
		Essential:    true,
		PortMappings: []portMapping{{ContainerPort: containerPortNumber, Protocol: "tcp"}},
		Environment:  pairs,
		LogConfig: logConfiguration{
			Driver: "awslogs",
			Options: map[string]string{
				"awslogs-group":         w.substrate.LogGroup,
				"awslogs-region":        w.region,
				"awslogs-stream-prefix": w.physical(),
			},
		},
	}})
	if err != nil {
		return "", fmt.Errorf("render %s's container definition: %w", w.app, err)
	}
	return string(encoded), nil
}

func (w *containerWork) run(ctx *pulumi.Context) error {
	physical := w.physical()
	tags := resourceTags(naming.KindService, "", w.tags)

	var taskRole pulumi.StringPtrInput
	var before []pulumi.Resource
	if len(w.policies) > 0 {
		role, granted, err := w.taskRole(ctx)
		if err != nil {
			return err
		}
		taskRole = role.Arn
		before = granted
	}

	definition, err := w.definition()
	if err != nil {
		return err
	}
	task, err := ecs.NewTaskDefinition(ctx, naming.ResourceID(naming.KindService, containerLocalName, "task"), &ecs.TaskDefinitionArgs{
		Family:                  pulumi.String(physical),
		Cpu:                     pulumi.String(containerCPU),
		Memory:                  pulumi.String(containerMemory),
		NetworkMode:             pulumi.String("awsvpc"),
		RequiresCompatibilities: pulumi.StringArray{pulumi.String("FARGATE")},
		ExecutionRoleArn:        pulumi.String(w.substrate.ExecutionRole),
		TaskRoleArn:             taskRole,
		ContainerDefinitions:    pulumi.ToSecret(pulumi.String(definition)).(pulumi.StringOutput),
		RuntimePlatform: &ecs.TaskDefinitionRuntimePlatformArgs{
			OperatingSystemFamily: pulumi.String("LINUX"),
			CpuArchitecture:       pulumi.String("X86_64"),
		},
		Tags: tags,
	}, pulumi.DependsOn(before))
	if err != nil {
		return err
	}

	group, err := lb.NewTargetGroup(ctx, naming.ResourceID(naming.KindService, containerLocalName, "targets"), &lb.TargetGroupArgs{
		NamePrefix:          pulumi.String(targetGroupNamePrefix),
		Port:                pulumi.Int(containerPortNumber),
		Protocol:            pulumi.String("HTTP"),
		TargetType:          pulumi.String("ip"),
		VpcId:               pulumi.String(w.substrate.VPC),
		DeregistrationDelay: pulumi.Int(deregistrationSeconds),
		HealthCheck: &lb.TargetGroupHealthCheckArgs{
			Enabled:            pulumi.Bool(true),
			Protocol:           pulumi.String("HTTP"),
			Path:               pulumi.String(w.healthPath),
			Matcher:            pulumi.String(healthyStatuses),
			Interval:           pulumi.Int(healthCheckIntervalSeconds),
			Timeout:            pulumi.Int(healthCheckTimeoutSeconds),
			HealthyThreshold:   pulumi.Int(healthyThreshold),
			UnhealthyThreshold: pulumi.Int(unhealthyThreshold),
		},
		Tags: tags,
	})
	if err != nil {
		return err
	}
	rule, err := lb.NewListenerRule(ctx, naming.ResourceID(naming.KindService, containerLocalName, "rule"), &lb.ListenerRuleArgs{
		ListenerArn: pulumi.String(w.substrate.Listener),
		Priority:    pulumi.Int(rulePriority(physical)),
		Conditions: lb.ListenerRuleConditionArray{
			&lb.ListenerRuleConditionArgs{HttpHeader: &lb.ListenerRuleConditionHttpHeaderArgs{
				HttpHeaderName: pulumi.String(edge.OriginSecretHeader),
				Values:         pulumi.StringArray{pulumi.String(w.secret)},
			}},
			&lb.ListenerRuleConditionArgs{HttpHeader: &lb.ListenerRuleConditionHttpHeaderArgs{
				HttpHeaderName: pulumi.String(edge.OriginContainerHeader),
				Values:         pulumi.StringArray{pulumi.String(physical)},
			}},
		},
		Actions: lb.ListenerRuleActionArray{&lb.ListenerRuleActionArgs{
			Type:           pulumi.String("forward"),
			TargetGroupArn: group.Arn,
		}},
		Tags: tags,
	})
	if err != nil {
		return err
	}

	service, err := ecs.NewService(ctx, naming.ResourceID(naming.KindService, containerLocalName), &ecs.ServiceArgs{
		Name:                          pulumi.String(physical),
		Cluster:                       pulumi.String(w.substrate.Cluster),
		TaskDefinition:                task.Arn,
		DesiredCount:                  pulumi.Int(1),
		LaunchType:                    pulumi.String("FARGATE"),
		HealthCheckGracePeriodSeconds: pulumi.Int(healthGraceSeconds),
		WaitForSteadyState:            pulumi.Bool(true),
		NetworkConfiguration: &ecs.ServiceNetworkConfigurationArgs{
			Subnets:        pulumi.ToStringArray(w.substrate.Subnets),
			SecurityGroups: pulumi.StringArray{pulumi.String(w.substrate.TaskSecurity)},
			AssignPublicIp: pulumi.Bool(true),
		},
		LoadBalancers: ecs.ServiceLoadBalancerArray{&ecs.ServiceLoadBalancerArgs{
			TargetGroupArn: group.Arn,
			ContainerName:  pulumi.String(containerName),
			ContainerPort:  pulumi.Int(containerPortNumber),
		}},
		Tags: tags,
	}, pulumi.DependsOn([]pulumi.Resource{rule}))
	if err != nil {
		return err
	}
	ctx.Export(w.app, pulumi.Map{
		outputKeyContainerURL:      pulumi.String("http://" + w.substrate.OriginHost),
		outputKeyContainerPhysical: service.Name,
	})
	return nil
}

func (w *containerWork) taskRole(ctx *pulumi.Context) (*iam.Role, []pulumi.Resource, error) {
	role, err := iam.NewRole(ctx, naming.ResourceID(naming.KindRole, roleLocalName), &iam.RoleArgs{
		NamePrefix:          pulumi.String(rolePrefix(w.role)),
		Description:         describe(w.role, "task role for this app's container"),
		AssumeRolePolicy:    pulumi.String(assumeRolePolicy(ecsTasksPrincipal)),
		PermissionsBoundary: permissionsBoundary(w.boundary),
		Tags:                resourceTags(naming.KindRole, "", w.tags),
	})
	if err != nil {
		return nil, nil, err
	}
	granted := make([]pulumi.Resource, 0, len(w.policies))
	for _, link := range w.policies {
		policy, err := iam.NewRolePolicy(ctx, naming.ResourceID(naming.KindRole, roleLocalName, "policy", "link", link.Link), &iam.RolePolicyArgs{
			Role:   role.Name,
			Policy: pulumi.String(link.Policy),
		})
		if err != nil {
			return nil, nil, err
		}
		granted = append(granted, policy)
	}
	return role, granted, nil
}

func runsContainer(plan providerkit.StackPlan) bool {
	return plan.App != nil && plan.App.Compute == providerkit.ComputeContainer
}

func (r *release) provisionContainer(ctx context.Context, plan providerkit.StackPlan, report providerkit.Reporter) (providerkit.StackResult, error) {
	work, err := r.checkContainer(plan)
	if err != nil {
		return providerkit.StackResult{}, err
	}
	held, err := r.ensureSubstrate(ctx, plan.Ref, report)
	if err != nil {
		return providerkit.StackResult{}, err
	}
	work.substrate = held
	plan.Options = work
	result, err := r.adapter.Run(ctx, plan, report)
	if err != nil {
		if released := r.releaseSubstrate(ctx, r.cfg.Records, plan.Ref, report); released != nil {
			return providerkit.StackResult{}, errors.Join(err, released)
		}
		return providerkit.StackResult{}, err
	}
	return result, nil
}

func (r *release) planContainer(ctx context.Context, plan providerkit.StackPlan, report providerkit.Reporter) (providerkit.Plan, error) {
	held, present, err := r.readSubstrate(ctx, plan.Ref.Class)
	if err != nil {
		return providerkit.Plan{}, err
	}
	if !present {
		return providerkit.Plan{Groups: []providerkit.ChangeGroup{
			{
				Kind:   providerkit.StackGroupKind,
				Name:   substrateRef(plan.Ref.Class).Name.String(),
				Action: providerkit.ActionCreate,
				Reason: "the first container deploy in the " + string(plan.Ref.Class) + " class stands up the load balancer and cluster every container app in it shares",
				Slow:   true,
			},
			{
				Kind:   providerkit.StackGroupKind,
				Name:   plan.Ref.Name.String(),
				Action: providerkit.ActionCreate,
				Reason: providerkit.DetailUnavailable,
				Slow:   true,
			},
		}}, nil
	}
	work, err := r.containerWork(plan, held)
	if err != nil {
		return providerkit.Plan{}, err
	}
	plan.Options = work
	return r.adapter.Preview(ctx, plan, report)
}

func (r *release) decodeContainer(work *containerWork, outputs auto.OutputMap) (providerkit.StackResult, error) {
	raw, produced := outputs[work.app]
	if !produced {
		return providerkit.StackResult{}, fmt.Errorf("stack produced no output for %s", work.app)
	}
	fields, mapped := raw.Value.(map[string]any)
	if !mapped {
		return providerkit.StackResult{}, fmt.Errorf("output for %s is not a map", work.app)
	}
	url, err := requireStringField(fields, work.app, outputKeyContainerURL)
	if err != nil {
		return providerkit.StackResult{}, err
	}
	physical, err := requireStringField(fields, work.app, outputKeyContainerPhysical)
	if err != nil {
		return providerkit.StackResult{}, err
	}
	return providerkit.StackResult{Containers: []providerkit.AppContainer{{
		Name:     work.app,
		Physical: physical,
		URL:      url,
		Image:    work.image,
	}}}, nil
}
