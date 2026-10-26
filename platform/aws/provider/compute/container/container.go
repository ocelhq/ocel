package container

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws/arn"
	elbv2 "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/appautoscaling"
	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/ecs"
	awsiam "github.com/pulumi/pulumi-aws/sdk/v7/go/aws/iam"
	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/lb"
	"github.com/pulumi/pulumi/sdk/v3/go/auto"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/arch"
	"github.com/ocelhq/ocel/pkg/containerimage"
	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/processenv"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/runtime/originguard"
	"github.com/ocelhq/ocel/platform/aws/provider/component"
	"github.com/ocelhq/ocel/platform/aws/provider/edges/alb"
	"github.com/ocelhq/ocel/platform/aws/provider/iam"
	"github.com/ocelhq/ocel/platform/aws/provider/queues"
	"github.com/ocelhq/ocel/platform/aws/provider/resources/topic"
	"github.com/ocelhq/ocel/platform/aws/provider/resources/variables"
	livefile "github.com/ocelhq/ocel/platform/aws/provider/resources/variables/live"
	"github.com/ocelhq/ocel/platform/aws/provider/site"
	"github.com/ocelhq/ocel/platform/aws/provider/tier/containers"
)

const rootHealthPath = "/"

const (
	containerPort    = "8080"
	CPU              = "512"
	Memory           = "1024"
	Name             = "app"
	containerPortEnv = "PORT"

	containerLocalName = "container"

	deregistrationSeconds = 10
	healthGraceSeconds    = 60

	healthCheckIntervalSeconds = 15
	healthCheckTimeoutSeconds  = 5
	healthyThreshold           = 2
	unhealthyThreshold         = 3
	healthyStatuses            = "200-399"

	OutputKeyURL      = "url"
	OutputKeyPhysical = "physical"

	maxRulePriority = 50000

	ScalingNamespace          = "ecs"
	ScalingDimension          = "ecs:service:DesiredCount"
	requestsPerTaskPerMinute  = 1000
	scaleOutCooldownSeconds   = 60
	scaleInCooldownSeconds    = 300
	requestCountPerTargetType = "ALBRequestCountPerTarget"

	minInstancesTag = "ocel:min-instances"
	maxInstancesTag = "ocel:max-instances"
)

type RulesAPI interface {
	DescribeRules(ctx context.Context, in *elbv2.DescribeRulesInput, opts ...func(*elbv2.Options)) (*elbv2.DescribeRulesOutput, error)
}

type Definition struct {
	App             string
	Arch            string
	Image           string
	Instances       provider.Instances
	Env             map[string]string
	Tags            map[string]string
	TransformTags   map[string]string
	Boundary        string
	RolePath        string
	Region          string
	Policies        []iam.BindingPolicy
	ManagedPolicies []string
	Variables       variables.Reader
	Cache           *site.ISR
	Infra           containers.Infrastructure

	AssetStack string
	AssetSets  []site.AssetSet

	tier             environment.Tier
	priority         int
	healthPath       string
	secrets          []string
	tasksPolicy      string
	functionPolicies map[string]string
	namespace        string
	service          naming.Coordinate
	role             naming.Coordinate
	public           bool
	workerApp        bool
}

type Target struct {
	Namespace            string
	Tier                 environment.Tier
	Region               string
	Slug                 string
	StateTable           topic.StateTable
	Variables            variables.Store
	VariablesTableARN    string
	AppBoundaryARN       string
	AppRolePath          string
	OriginSecret         string
	PreviousOriginSecret string
	ISR                  *site.ISR
	Buckets              naming.Buckets
	ImageOptimizerURL    string
}

func (d *Definition) Priority() int { return d.priority }

func (d *Definition) TaskSecurityGroups() pulumi.StringArray {
	return pulumi.StringArray{pulumi.String(d.Infra.TaskSecurity), pulumi.String(d.Infra.Clients)}
}

func (d *Definition) readListenerArn() string {
	if d.public {
		return d.Infra.PublicListener
	}
	return d.Infra.Listener
}

func (d *Definition) readServiceURL() string {
	if d.public {
		return "https://" + d.Infra.PublicHost
	}
	return "http://" + d.Infra.OriginHost
}

func IsRoutedPublicly(spec provider.StackSpec) bool {
	return spec.App != nil && spec.App.Router == alb.Kind
}

var fargateCPUArchitectures = map[string]string{
	arch.X8664: "X86_64",
	arch.ARM64: "ARM64",
}

func CPUArchitecture(declared string) string {
	return fargateCPUArchitectures[arch.Architecture(declared)]
}

func Arch(app, declared string) (string, error) {
	runs, known := arch.GoArch(declared)
	if !known {
		return "", refusal.Refuse(refusal.CodeInvalid,
			"app %s declares arch %q, and a container runs on %s or %s alone", app, declared, arch.X8664, arch.ARM64)
	}
	return runs, nil
}

func (d *Definition) readsLive() bool { return d.Variables.TableARN != "" }

type containerDefinition struct {
	Name         string            `json:"name"`
	Image        string            `json:"image"`
	Essential    bool              `json:"essential"`
	PortMappings []portMapping     `json:"portMappings"`
	Environment  []EnvironmentPair `json:"environment"`
	LogConfig    LogConfiguration  `json:"logConfiguration"`
}

type portMapping struct {
	ContainerPort int    `json:"containerPort"`
	Protocol      string `json:"protocol"`
}

type EnvironmentPair struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type LogConfiguration struct {
	Driver  string            `json:"logDriver"`
	Options map[string]string `json:"options"`
}

func NewDefinition(spec provider.StackSpec, at Target) (*Definition, error) {
	app := spec.App
	project, stack := naming.Sanitize(spec.Ref.Project), spec.Ref.Name
	if strings.TrimSpace(app.Image) == "" {
		return nil, refusal.Refuse(refusal.CodeInvalid,
			"app %s names no image, and a container on this provider runs what a registry coordinate names and nothing else", app.App)
	}
	healthPath := app.HealthCheckPath
	if healthPath == "" {
		healthPath = rootHealthPath
	}
	if !containerimage.IsHealthCheckPath(healthPath) {
		return nil, refusal.Refuse(refusal.CodeInvalid,
			"app %s is probed at %q, which is not a path a load balancer can send a health check to", app.App, healthPath)
	}
	if at.OriginSecret == "" {
		return nil, refusal.Refuse(refusal.CodeNotReady,
			"this tier has no origin secret, and a container answers only to an edge that presents one: re-run `%s`", provider.BootstrapCommand(spec.Ref.Tier))
	}
	if at.AppBoundaryARN == "" {
		return nil, refusal.Refuse(refusal.CodeNotReady,
			"this deploy resolved no app boundary for %s, and a task role made without one is capped by nothing: re-run `%s`", app.App, provider.BootstrapCommand(spec.Ref.Tier))
	}
	policies, err := iam.NewBindingPolicies(app.Grants)
	if err != nil {
		return nil, err
	}
	bundle, err := variables.SealApp(at.Variables, spec.Ref.Project, app.App, liveOnly(app.Values))
	if err != nil {
		return nil, err
	}
	env, err := containerEnv(app.App, app.Values, bundle.Live)
	if err != nil {
		return nil, err
	}
	if !IsRoutedPublicly(spec) {
		component.GuardOrigin(env, at.OriginSecret, at.PreviousOriginSecret)
	}
	tasksPolicyDoc := ""
	if topic.ReachesTasks(app) {
		topics, err := topic.Of(queues.Names{Namespace: at.Namespace, Project: project, Env: stack.Env}, app.Topics)
		if err != nil {
			return nil, err
		}
		served, err := WorkerServices(at.Namespace, at.Tier, project, stack.Env, app.DeploymentWorkers)
		if err != nil {
			return nil, err
		}
		topology, err := topic.QueueTopology(at.StateTable, project, stack.Env, topics, served)
		if err != nil {
			return nil, err
		}
		rendered, err := queues.Render(topology)
		if err != nil {
			return nil, err
		}
		if len(rendered) > 0 {
			env[queues.EnvVar] = string(rendered)
		}
		wakes, err := WorkerServiceARNs(at.StateTable, at.Namespace, at.Tier, project, stack.Env, app.DeploymentWorkers)
		if err != nil {
			return nil, err
		}
		if tasksPolicyDoc, err = topic.TasksPolicy(at.StateTable, project, stack.Env, topics, wakes); err != nil {
			return nil, err
		}
	}
	cache := newNextCache(app, at)
	if cache != nil {
		maps.Copy(env, cache.Env())
		if spec.Edge != nil && !IsRoutedPublicly(spec) {
			maps.Copy(env, edge.NewCachingOriginEnv(spec.Edge.Facts(), stack.Release.String()))
		}
	}
	var functionPolicies map[string]string
	if app.IsBuiltFromFunction() {
		maps.Copy(env, newFunctionEnv(app, at))
		if functionPolicies, err = newFunctionPolicies(app, at); err != nil {
			return nil, err
		}
	}
	reader := variables.Reader{KeyARN: at.Variables.KeyARN}
	if bundle.HasLive() {
		reader.TableARN = at.VariablesTableARN
		reader.Referenced = bundle.Referenced
		reader.Slug = at.Slug
		reader.Tier = string(at.Tier)
	}
	return &Definition{
		App:              app.App,
		Arch:             app.Arch,
		Image:            app.Image,
		healthPath:       healthPath,
		Instances:        app.Instances,
		Env:              env,
		Tags:             spec.Tags,
		Boundary:         at.AppBoundaryARN,
		RolePath:         at.AppRolePath,
		Region:           at.Region,
		tier:             at.Tier,
		secrets:          presentedSecrets(at.OriginSecret, at.PreviousOriginSecret),
		Policies:         policies,
		tasksPolicy:      tasksPolicyDoc,
		functionPolicies: functionPolicies,
		namespace:        at.Namespace,
		Variables:        reader,
		Cache:            cache,
		service:          serviceCoordinate(project, stack),
		role:             component.AppRoleCoordinate(project, stack),
		public:           IsRoutedPublicly(spec),
		workerApp:        provider.IsWorkerApp(app.App, app.Workers),
	}, nil
}

func newNextCache(app *provider.AppSpec, at Target) *site.ISR {
	if !app.IsBuiltFromFunction() {
		return nil
	}
	return at.ISR
}

func liveOnly(values provider.AppValues) provider.AppValues {
	values.Sensitive = nil
	return values
}

func presentedSecrets(current, previous string) []string {
	if previous == "" {
		return []string{current}
	}
	return []string{current, previous}
}

func containerEnv(app string, values provider.AppValues, manifest []byte) (map[string]string, error) {
	env := make(map[string]string, len(values.Plain)+len(values.Sensitive)+8)
	maps.Copy(env, values.Plain)
	maps.Copy(env, values.Sensitive)
	if values.Folder != "" {
		env[processenv.AppFolderEnvVar] = values.Folder
	}
	maps.Copy(env, values.PhaseEnv())
	if _, set := env[containerPortEnv]; set {
		return nil, refusal.Refuse(refusal.CodeInvalid,
			"app %s sets %s, and a container on this provider listens on %s, which it is handed as %s: drop the value and read the port from the environment",
			app, containerPortEnv, containerPort, containerPortEnv)
	}
	env[containerPortEnv] = containerPort
	if len(manifest) > 0 {
		env[livefile.EnvVar] = string(manifest)
	}
	return env, nil
}

func (d *Definition) definitionEnv() map[string]string {
	env := make(map[string]string, len(d.Env)+1)
	maps.Copy(env, d.Env)
	env[originguard.HealthPathVar] = d.healthPath
	return env
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

func (d *Definition) names() (Names, error) {
	return namesAt(d.namespace, d.service, d.role)
}

func (d *Definition) definition(service string) (string, error) {
	env := d.definitionEnv()
	pairs := make([]EnvironmentPair, 0, len(env))
	for _, key := range slices.Sorted(maps.Keys(env)) {
		pairs = append(pairs, EnvironmentPair{Name: key, Value: env[key]})
	}
	encoded, err := json.Marshal([]containerDefinition{{
		Name:         Name,
		Image:        d.Image,
		Essential:    true,
		PortMappings: []portMapping{{ContainerPort: containers.TaskPort, Protocol: "tcp"}},
		Environment:  pairs,
		LogConfig: LogConfiguration{
			Driver: "awslogs",
			Options: map[string]string{
				"awslogs-group":         d.Infra.LogGroup,
				"awslogs-region":        d.Region,
				"awslogs-stream-prefix": service,
			},
		},
	}})
	if err != nil {
		return "", fmt.Errorf("render %s's container definition: %w", d.App, err)
	}
	return string(encoded), nil
}

func (d *Definition) Declare(ctx *pulumi.Context) error {
	tags := component.Tags(naming.KindService, "", d.taggedWith(d.TransformTags))

	if d.workerApp {
		return nil
	}
	names, err := d.names()
	if err != nil {
		return err
	}
	physical := names.Service

	var taskRole pulumi.StringPtrInput
	var before []pulumi.Resource
	if len(d.Policies) > 0 || len(d.ManagedPolicies) > 0 || d.tasksPolicy != "" || d.readsLive() || d.Cache != nil || len(d.functionPolicies) > 0 {
		role, granted, err := d.taskRole(ctx, names.Role)
		if err != nil {
			return err
		}
		taskRole = role.Arn
		before = granted
	}
	pushed, err := site.DeclareAssetSets(ctx, d.AssetStack, d.AssetSets)
	if err != nil {
		return err
	}
	before = append(before, pushed...)

	definition, err := d.definition(physical)
	if err != nil {
		return err
	}
	task, err := ecs.NewTaskDefinition(ctx, physical, &ecs.TaskDefinitionArgs{
		Family:                  pulumi.String(physical),
		Cpu:                     pulumi.String(CPU),
		Memory:                  pulumi.String(Memory),
		NetworkMode:             pulumi.String("awsvpc"),
		RequiresCompatibilities: pulumi.StringArray{pulumi.String("FARGATE")},
		ExecutionRoleArn:        pulumi.String(d.Infra.ExecutionRole),
		TaskRoleArn:             taskRole,
		ContainerDefinitions:    pulumi.ToSecret(pulumi.String(definition)).(pulumi.StringOutput),
		RuntimePlatform: &ecs.TaskDefinitionRuntimePlatformArgs{
			OperatingSystemFamily: pulumi.String("LINUX"),
			CpuArchitecture:       pulumi.String(CPUArchitecture(d.Arch)),
		},
		Tags: tags,
	}, pulumi.DependsOn(before))
	if err != nil {
		return err
	}

	group, err := lb.NewTargetGroup(ctx, names.TargetGroup, &lb.TargetGroupArgs{
		Name:                pulumi.String(names.TargetGroup),
		Port:                pulumi.Int(containers.TaskPort),
		Protocol:            pulumi.String("HTTP"),
		TargetType:          pulumi.String("ip"),
		VpcId:               pulumi.String(d.Infra.VPC),
		DeregistrationDelay: pulumi.Int(deregistrationSeconds),
		HealthCheck: &lb.TargetGroupHealthCheckArgs{
			Enabled:            pulumi.Bool(true),
			Protocol:           pulumi.String("HTTP"),
			Path:               pulumi.String(d.healthPath),
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
	rule, err := lb.NewListenerRule(ctx, names.Rule, &lb.ListenerRuleArgs{
		ListenerArn: pulumi.String(d.readListenerArn()),
		Priority:    pulumi.Int(d.priority),
		Conditions: lb.ListenerRuleConditionArray{
			&lb.ListenerRuleConditionArgs{HttpHeader: &lb.ListenerRuleConditionHttpHeaderArgs{
				HttpHeaderName: pulumi.String(edge.OriginSecretHeader),
				Values:         pulumi.ToStringArray(d.secrets),
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

	service, err := ecs.NewService(ctx, physical, &ecs.ServiceArgs{
		Name:                          pulumi.String(physical),
		Cluster:                       pulumi.String(d.Infra.Cluster),
		TaskDefinition:                task.Arn,
		DesiredCount:                  pulumi.Int(d.Instances.Min),
		LaunchType:                    pulumi.String("FARGATE"),
		HealthCheckGracePeriodSeconds: pulumi.Int(healthGraceSeconds),
		WaitForSteadyState:            pulumi.Bool(true),
		NetworkConfiguration: &ecs.ServiceNetworkConfigurationArgs{
			Subnets:        pulumi.ToStringArray(d.Infra.Subnets),
			SecurityGroups: d.TaskSecurityGroups(),
			AssignPublicIp: pulumi.Bool(true),
		},
		LoadBalancers: ecs.ServiceLoadBalancerArray{&ecs.ServiceLoadBalancerArgs{
			TargetGroupArn: group.Arn,
			ContainerName:  pulumi.String(Name),
			ContainerPort:  pulumi.Int(containers.TaskPort),
		}},
		Tags: tags,
	}, pulumi.DependsOn([]pulumi.Resource{rule}), pulumi.IgnoreChanges([]string{"desiredCount"}))
	if err != nil {
		return err
	}
	target, err := d.declareScalableTarget(ctx, physical, service, tags)
	if err != nil {
		return err
	}
	if d.scales() {
		if err := d.scaleOnRequests(ctx, physical, target, group); err != nil {
			return err
		}
	}
	ctx.Export(d.App, pulumi.Map{
		OutputKeyURL:      pulumi.String(d.readServiceURL()),
		OutputKeyPhysical: service.Name,
	})
	return nil
}

func (d *Definition) scales() bool { return d.Instances.Max > d.Instances.Min }

func (d *Definition) declareScalableTarget(ctx *pulumi.Context, physical string, service *ecs.Service, tags pulumi.StringMap) (*appautoscaling.Target, error) {
	clusterARN, err := arn.Parse(d.Infra.Cluster)
	cluster, found := strings.CutPrefix(clusterARN.Resource, "cluster/")
	if err != nil || !found {
		return nil, fmt.Errorf("the container cluster %q is not an ECS cluster ARN, so %s's service has no scalable target to name", d.Infra.Cluster, d.App)
	}
	targetTags := pulumi.StringMap{
		minInstancesTag: pulumi.String(strconv.Itoa(d.Instances.Min)),
		maxInstancesTag: pulumi.String(strconv.Itoa(d.Instances.Max)),
	}
	maps.Copy(targetTags, tags)
	resourceID := pulumi.Sprintf("service/%s/%s", cluster, service.Name)
	return appautoscaling.NewTarget(ctx, physical+component.PartSeparator+scalingPart, &appautoscaling.TargetArgs{
		ServiceNamespace:  pulumi.String(ScalingNamespace),
		ScalableDimension: pulumi.String(ScalingDimension),
		ResourceId:        resourceID,
		MinCapacity:       pulumi.Int(d.Instances.Min),
		MaxCapacity:       pulumi.Int(d.Instances.Max),
		Tags:              targetTags,
	}, pulumi.IgnoreChanges([]string{"minCapacity", "maxCapacity"}))
}

func (d *Definition) scaleOnRequests(ctx *pulumi.Context, physical string, target *appautoscaling.Target, group *lb.TargetGroup) error {
	balancer, err := parseBalancerSuffix(d.readListenerArn())
	if err != nil {
		return err
	}
	_, err = appautoscaling.NewPolicy(ctx, physical+component.PartSeparator+scalingPart+naming.WordSeparator+"requests", &appautoscaling.PolicyArgs{
		PolicyType:        pulumi.String("TargetTrackingScaling"),
		ServiceNamespace:  target.ServiceNamespace,
		ScalableDimension: target.ScalableDimension,
		ResourceId:        target.ResourceId,
		TargetTrackingScalingPolicyConfiguration: &appautoscaling.PolicyTargetTrackingScalingPolicyConfigurationArgs{
			TargetValue:      pulumi.Float64(requestsPerTaskPerMinute),
			ScaleOutCooldown: pulumi.Int(scaleOutCooldownSeconds),
			ScaleInCooldown:  pulumi.Int(scaleInCooldownSeconds),
			PredefinedMetricSpecification: &appautoscaling.PolicyTargetTrackingScalingPolicyConfigurationPredefinedMetricSpecificationArgs{
				PredefinedMetricType: pulumi.String(requestCountPerTargetType),
				ResourceLabel:        pulumi.Sprintf("%s/%s", balancer, group.ArnSuffix),
			},
		},
	})
	return err
}

func parseBalancerSuffix(listener string) (string, error) {
	parsed, err := arn.Parse(listener)
	parts := strings.Split(parsed.Resource, "/")
	if err != nil || len(parts) != 5 || parts[0] != "listener" || parts[1] != "app" {
		return "", fmt.Errorf("the container front's listener %q is not an application load balancer listener ARN, so requests per task cannot be counted against it", listener)
	}
	return strings.Join(parts[1:4], "/"), nil
}

func (d *Definition) taggedWith(extra map[string]string) map[string]string {
	if len(extra) == 0 {
		return d.Tags
	}
	tags := make(map[string]string, len(d.Tags)+len(extra))
	maps.Copy(tags, d.Tags)
	maps.Copy(tags, extra)
	return tags
}

func (d *Definition) taskRole(ctx *pulumi.Context, name component.Name) (*awsiam.Role, []pulumi.Resource, error) {
	documents := map[string]pulumi.StringInput{}
	for _, binding := range d.Policies {
		documents[component.BindingPolicyName(binding.Binding)] = pulumi.String(binding.Policy)
	}
	if d.tasksPolicy != "" {
		documents[component.TasksPolicyName] = pulumi.String(d.tasksPolicy)
	}
	if d.Cache != nil {
		document, err := d.Cache.Policy()
		if err != nil {
			return nil, nil, err
		}
		documents[component.ISRCachePolicyName] = pulumi.String(document)
	}
	for name, document := range d.functionPolicies {
		documents[name] = pulumi.String(document)
	}
	if d.readsLive() {
		document, err := variables.ReadPolicy(d.Variables)
		if err != nil {
			return nil, nil, err
		}
		documents[component.VariablesPolicyName] = pulumi.String(document)
	}
	return component.NewAppRole(ctx, component.AppRole{
		Name:        name,
		Description: component.Describe(d.role, "task role for this app's container"),
		Principal:   iam.ECSTasksPrincipal,
		Boundary:    d.Boundary,
		Path:        d.RolePath,
		Tags:        d.taggedWith(d.TransformTags),
		Managed:     d.ManagedPolicies,
		Policies:    documents,
	})
}

func (d *Definition) Decode(outputs auto.OutputMap) (provider.StackResult, error) {
	if d.workerApp {
		return provider.StackResult{}, nil
	}
	fields, err := component.RequireOutputFields(outputs, d.App)
	if err != nil {
		return provider.StackResult{}, err
	}
	url, err := component.RequireStringField(fields, d.App, OutputKeyURL)
	if err != nil {
		return provider.StackResult{}, err
	}
	physical, err := component.RequireStringField(fields, d.App, OutputKeyPhysical)
	if err != nil {
		return provider.StackResult{}, err
	}
	result := provider.StackResult{Containers: []provider.AppContainer{{
		Name:     d.App,
		Physical: physical,
		URL:      url,
		Image:    d.Image,
	}}}
	if d.Cache != nil {
		result.PurgeSecret = d.Cache.PurgeSecret
	}
	return result, nil
}
