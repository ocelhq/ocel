package deploy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/ocelhq/ocel/platform/aws/provider/edges/alb"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/arn"
	elbv2 "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	elbv2types "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2/types"
	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/appautoscaling"
	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/ecs"
	iam "github.com/pulumi/pulumi-aws/sdk/v7/go/aws/iam"
	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/lb"
	"github.com/pulumi/pulumi/sdk/v3/go/auto"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/arch"
	"github.com/ocelhq/ocel/pkg/containerimage"
	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/processenv"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/runtime/originguard"
	variables "github.com/ocelhq/ocel/platform/aws/provider/variables/live"
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

	scalingNamespace          = "ecs"
	scalingDimension          = "ecs:service:DesiredCount"
	requestsPerTaskPerMinute  = 1000
	scaleOutCooldownSeconds   = 60
	scaleInCooldownSeconds    = 300
	requestCountPerTargetType = "ALBRequestCountPerTarget"
)

type RulesAPI interface {
	DescribeRules(ctx context.Context, in *elbv2.DescribeRulesInput, opts ...func(*elbv2.Options)) (*elbv2.DescribeRulesOutput, error)
}

type containerWork struct {
	priority    int
	app         string
	arch        string
	image       string
	healthPath  string
	instances   provider.Instances
	env         map[string]string
	tags        map[string]string
	boundary    string
	region      string
	secrets     []string
	policies    []bindingPolicy
	values      executionRole
	transformed *transformPatches
	service     naming.Coordinate
	role        naming.Coordinate
	infra       containerInfra
	public      bool
}

func (w *containerWork) readListenerArn() string {
	if w.public {
		return w.infra.PublicListener
	}
	return w.infra.Listener
}

func (w *containerWork) readServiceURL() string {
	if w.public {
		return "https://" + w.infra.PublicHost
	}
	return "http://" + w.infra.OriginHost
}

func isRoutedPublicly(spec provider.StackSpec) bool {
	return spec.App != nil && spec.App.Router == alb.Kind
}

var fargateCPUArchitectures = map[string]string{
	arch.X8664: "X86_64",
	arch.ARM64: "ARM64",
}

func fargateCPUArchitecture(declared string) string {
	return fargateCPUArchitectures[arch.Architecture(declared)]
}

func ContainerArch(app, declared string) (string, error) {
	runs, known := arch.GoArch(declared)
	if !known {
		return "", refusal.Refuse(refusal.CodeInvalid,
			"app %s declares arch %q, and a container runs on %s or %s alone", app, declared, arch.X8664, arch.ARM64)
	}
	return runs, nil
}

func (w *containerWork) readsLive() bool { return w.values.ValuesTableARN != "" }

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

func (r *release) checkContainer(spec provider.StackSpec) (*containerWork, error) {
	app := spec.App
	project, stack := naming.Sanitize(spec.Ref.Project), spec.Ref.Name
	if strings.TrimSpace(app.Image) == "" {
		return nil, refusal.Refuse(refusal.CodeInvalid,
			"app %s names no image, and a container on this provider runs what a registry coordinate names and nothing else", app.App)
	}
	if !containerimage.IsHealthCheckPath(app.HealthCheckPath) {
		return nil, refusal.Refuse(refusal.CodeInvalid,
			"app %s is probed at %q, which is not a path a load balancer can send a health check to", app.App, app.HealthCheckPath)
	}
	if r.cfg.OriginSecret == "" {
		return nil, refusal.Refuse(refusal.CodeNotReady,
			"this tier has no origin secret, and a container answers only to an edge that presents one: re-run `%s`", provider.BootstrapCommand(spec.Ref.Tier))
	}
	if r.cfg.AppBoundaryARN == "" {
		return nil, refusal.Refuse(refusal.CodeNotReady,
			"this deploy resolved no app boundary for %s, and a task role made without one is capped by nothing: re-run `%s`", app.App, provider.BootstrapCommand(spec.Ref.Tier))
	}
	policies, err := planBindingPolicies(app.Grants)
	if err != nil {
		return nil, err
	}
	bundle, err := r.sealApp(spec.Ref.Project, app.App, liveOnly(app.Values))
	if err != nil {
		return nil, err
	}
	guard, previousGuard := r.cfg.OriginSecret, r.cfg.PreviousOriginSecret
	if isRoutedPublicly(spec) {
		guard, previousGuard = "", ""
	}
	env, err := containerEnv(app.App, app.Values, guard, previousGuard, bundle.Live)
	if err != nil {
		return nil, err
	}
	values := executionRole{App: app.App, VariablesKeyARN: r.cfg.VariablesKeyARN}
	if bundle.hasLive() {
		values.ValuesTableARN = r.cfg.VariablesTableARN
		values.VariablesReferenced = bundle.Referenced
		values.Slug = r.cfg.Slug
		values.VariablesTier = string(r.cfg.Tier)
	}
	return &containerWork{
		app:        app.App,
		arch:       app.Arch,
		image:      app.Image,
		healthPath: app.HealthCheckPath,
		instances:  app.Instances,
		env:        env,
		tags:       spec.Tags,
		boundary:   r.cfg.AppBoundaryARN,
		region:     r.cfg.Region,
		secrets:    presentedSecrets(r.cfg.OriginSecret, r.cfg.PreviousOriginSecret),
		policies:   policies,
		values:     values,
		service:    serviceCoordinate(project, stack),
		role:       roleCoordinate(project, stack),
		public:     isRoutedPublicly(spec),
	}, nil
}

func liveOnly(values provider.AppValues) provider.AppValues {
	values.Sensitive = nil
	return values
}

func (r *release) containerWork(spec provider.StackSpec, infra containerInfra) (*containerWork, error) {
	work, err := r.checkContainer(spec)
	if err != nil {
		return nil, err
	}
	work.infra = infra
	return work, nil
}

func rulePriority(physical string, taken map[int]bool) int {
	sum := fnv.New32a()
	_, _ = sum.Write([]byte(physical))
	start := int(sum.Sum32() % uint32(maxRulePriority))
	for step := range maxRulePriority {
		priority := 1 + (start+step)%maxRulePriority
		if !taken[priority] {
			return priority
		}
	}
	return 0
}

func takenPriorities(ctx context.Context, rules RulesAPI, listener, physical string) (map[int]bool, error) {
	taken := map[int]bool{}
	if rules == nil {
		return taken, nil
	}
	var marker *string
	for {
		page, err := rules.DescribeRules(ctx, &elbv2.DescribeRulesInput{ListenerArn: aws.String(listener), Marker: marker})
		if err != nil {
			return nil, fmt.Errorf("read the rules already on the container front: %w", err)
		}
		for _, rule := range page.Rules {
			if routesContainer(rule, physical) {
				continue
			}
			if priority, err := strconv.Atoi(aws.ToString(rule.Priority)); err == nil {
				taken[priority] = true
			}
		}
		if marker = page.NextMarker; marker == nil {
			return taken, nil
		}
	}
}

func routesContainer(rule elbv2types.Rule, physical string) bool {
	for _, condition := range rule.Conditions {
		header := condition.HttpHeaderConfig
		if header == nil || !strings.EqualFold(aws.ToString(header.HttpHeaderName), edge.OriginContainerHeader) {
			continue
		}
		if slices.Contains(header.Values, physical) {
			return true
		}
	}
	return false
}

func (r *release) placeRule(ctx context.Context, work *containerWork) error {
	taken, err := takenPriorities(ctx, r.cfg.Rules, work.readListenerArn(), work.physical())
	if err != nil {
		return err
	}
	if work.priority = rulePriority(work.physical(), taken); work.priority == 0 {
		return refusal.Refuse(refusal.CodeInvalid,
			"the container front for the %s tier has a rule at every priority a listener allows, so %s has no slot: prune the releases behind it", r.cfg.Tier, work.app)
	}
	return nil
}

const (
	priorityInUseCode = "PriorityInUse"
	rulePlacements    = 3
)

func priorityTaken(err error) bool {
	return err != nil && strings.Contains(err.Error(), priorityInUseCode)
}

func presentedSecrets(current, previous string) []string {
	if previous == "" {
		return []string{current}
	}
	return []string{current, previous}
}

func containerEnv(app string, values provider.AppValues, originSecret, previousSecret string, manifest []byte) (map[string]string, error) {
	env := make(map[string]string, len(values.Plain)+len(values.Sensitive)+7)
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
	if originSecret != "" {
		env[edge.OriginSecretVar] = originSecret
	}
	if previousSecret != "" {
		env[edge.OriginSecretPreviousVar] = previousSecret
	}
	if len(manifest) > 0 {
		env[variables.EnvVar] = string(manifest)
	}
	return env, nil
}

func (w *containerWork) definitionEnv() map[string]string {
	env := make(map[string]string, len(w.env)+1)
	maps.Copy(env, w.env)
	env[originguard.HealthPathVar] = w.healthPath
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

func (w *containerWork) physical() string {
	return w.service.PhysicalName(maxLambdaNameLen)
}

func (w *containerWork) definition() (string, error) {
	env := w.definitionEnv()
	pairs := make([]environmentPair, 0, len(env))
	for _, key := range slices.Sorted(maps.Keys(env)) {
		pairs = append(pairs, environmentPair{Name: key, Value: env[key]})
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
				"awslogs-group":         w.infra.LogGroup,
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
	tags := resourceTags(naming.KindService, "", w.taggedWith(w.transformed.tagsFor(transformTypeContainer, w.app)))

	if err := w.transformed.install(ctx); err != nil {
		return err
	}

	var taskRole pulumi.StringPtrInput
	var before []pulumi.Resource
	if len(w.policies) > 0 || w.readsLive() {
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
		ExecutionRoleArn:        pulumi.String(w.infra.ExecutionRole),
		TaskRoleArn:             taskRole,
		ContainerDefinitions:    pulumi.ToSecret(pulumi.String(definition)).(pulumi.StringOutput),
		RuntimePlatform: &ecs.TaskDefinitionRuntimePlatformArgs{
			OperatingSystemFamily: pulumi.String("LINUX"),
			CpuArchitecture:       pulumi.String(fargateCPUArchitecture(w.arch)),
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
		VpcId:               pulumi.String(w.infra.VPC),
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
		ListenerArn: pulumi.String(w.readListenerArn()),
		Priority:    pulumi.Int(w.priority),
		Conditions: lb.ListenerRuleConditionArray{
			&lb.ListenerRuleConditionArgs{HttpHeader: &lb.ListenerRuleConditionHttpHeaderArgs{
				HttpHeaderName: pulumi.String(edge.OriginSecretHeader),
				Values:         pulumi.ToStringArray(w.secrets),
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

	serviceOptions := []pulumi.ResourceOption{pulumi.DependsOn([]pulumi.Resource{rule})}
	if w.scales() {
		serviceOptions = append(serviceOptions, pulumi.IgnoreChanges([]string{"desiredCount"}))
	}
	service, err := ecs.NewService(ctx, naming.ResourceID(naming.KindService, containerLocalName), &ecs.ServiceArgs{
		Name:                          pulumi.String(physical),
		Cluster:                       pulumi.String(w.infra.Cluster),
		TaskDefinition:                task.Arn,
		DesiredCount:                  pulumi.Int(w.instances.Min),
		LaunchType:                    pulumi.String("FARGATE"),
		HealthCheckGracePeriodSeconds: pulumi.Int(healthGraceSeconds),
		WaitForSteadyState:            pulumi.Bool(true),
		NetworkConfiguration: &ecs.ServiceNetworkConfigurationArgs{
			Subnets:        pulumi.ToStringArray(w.infra.Subnets),
			SecurityGroups: pulumi.StringArray{pulumi.String(w.infra.TaskSecurity)},
			AssignPublicIp: pulumi.Bool(true),
		},
		LoadBalancers: ecs.ServiceLoadBalancerArray{&ecs.ServiceLoadBalancerArgs{
			TargetGroupArn: group.Arn,
			ContainerName:  pulumi.String(containerName),
			ContainerPort:  pulumi.Int(containerPortNumber),
		}},
		Tags: tags,
	}, serviceOptions...)
	if err != nil {
		return err
	}
	if w.scales() {
		if err := w.scaleOnRequests(ctx, service, group, tags); err != nil {
			return err
		}
	}
	ctx.Export(w.app, pulumi.Map{
		outputKeyContainerURL:      pulumi.String(w.readServiceURL()),
		outputKeyContainerPhysical: service.Name,
	})
	return nil
}

func (w *containerWork) scales() bool { return w.instances.Max > w.instances.Min }

func (w *containerWork) scaleOnRequests(ctx *pulumi.Context, service *ecs.Service, group *lb.TargetGroup, tags pulumi.StringMap) error {
	clusterARN, err := arn.Parse(w.infra.Cluster)
	cluster, found := strings.CutPrefix(clusterARN.Resource, "cluster/")
	if err != nil || !found {
		return fmt.Errorf("the container cluster %q is not an ECS cluster ARN, so %s's service has no scalable target to name", w.infra.Cluster, w.app)
	}
	balancer, err := parseBalancerSuffix(w.readListenerArn())
	if err != nil {
		return err
	}
	resourceID := pulumi.Sprintf("service/%s/%s", cluster, service.Name)
	target, err := appautoscaling.NewTarget(ctx, naming.ResourceID(naming.KindService, containerLocalName, "scaling"), &appautoscaling.TargetArgs{
		ServiceNamespace:  pulumi.String(scalingNamespace),
		ScalableDimension: pulumi.String(scalingDimension),
		ResourceId:        resourceID,
		MinCapacity:       pulumi.Int(w.instances.Min),
		MaxCapacity:       pulumi.Int(w.instances.Max),
		Tags:              tags,
	})
	if err != nil {
		return err
	}
	_, err = appautoscaling.NewPolicy(ctx, naming.ResourceID(naming.KindService, containerLocalName, "scaling", "requests"), &appautoscaling.PolicyArgs{
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

func (w *containerWork) taggedWith(extra map[string]string) map[string]string {
	if len(extra) == 0 {
		return w.tags
	}
	tags := make(map[string]string, len(w.tags)+len(extra))
	maps.Copy(tags, w.tags)
	maps.Copy(tags, extra)
	return tags
}

func (w *containerWork) taskRole(ctx *pulumi.Context) (*iam.Role, []pulumi.Resource, error) {
	role, err := iam.NewRole(ctx, naming.ResourceID(naming.KindRole, roleLocalName), &iam.RoleArgs{
		NamePrefix:          pulumi.String(rolePrefix(w.role)),
		Description:         describe(w.role, "task role for this app's container"),
		AssumeRolePolicy:    pulumi.String(assumeRolePolicy(ecsTasksPrincipal)),
		PermissionsBoundary: pulumi.String(w.boundary),
		Tags:                resourceTags(naming.KindRole, "", w.taggedWith(w.transformed.tagsFor(transformTypeContainer, w.app))),
	})
	if err != nil {
		return nil, nil, err
	}
	granted := make([]pulumi.Resource, 0, len(w.policies))
	for _, binding := range w.policies {
		policy, err := iam.NewRolePolicy(ctx, naming.ResourceID(naming.KindRole, roleLocalName, "policy", "binding", binding.Binding), &iam.RolePolicyArgs{
			Role:   role.Name,
			Policy: pulumi.String(binding.Policy),
		})
		if err != nil {
			return nil, nil, err
		}
		granted = append(granted, policy)
	}
	if w.readsLive() {
		rendered, err := variablesReadPolicy(w.values)
		if err != nil {
			return nil, nil, err
		}
		policy, err := iam.NewRolePolicy(ctx, naming.ResourceID(naming.KindRole, roleLocalName, "policy", "variables", "read"), &iam.RolePolicyArgs{
			Role:   role.Name,
			Policy: pulumi.String(rendered),
		})
		if err != nil {
			return nil, nil, err
		}
		granted = append(granted, policy)
	}
	return role, granted, nil
}

func runsContainer(spec provider.StackSpec) bool {
	return spec.App != nil && spec.App.Compute == provider.ComputeContainer
}

func (r *release) provisionContainer(ctx context.Context, spec provider.StackSpec, progress progress.Log) (provider.StackResult, error) {
	work, err := r.checkContainer(spec)
	if err != nil {
		return provider.StackResult{}, err
	}
	if work.transformed, err = transformStackSpec(ctx, r.cfg.Transform, spec); err != nil {
		return provider.StackResult{}, err
	}
	public, err := readPublicRanges(spec)
	if err != nil {
		return provider.StackResult{}, err
	}
	infra, err := r.ensureContainerInfra(ctx, spec.Ref, public, progress)
	if err != nil {
		return provider.StackResult{}, err
	}
	work.infra = infra
	result, err := r.runContainer(ctx, spec, work, progress)
	if err != nil {
		return provider.StackResult{}, errors.Join(err, r.abandonContainer(ctx, spec.Ref, progress))
	}
	if err := work.transformed.refuseUnclaimed(); err != nil {
		return provider.StackResult{}, errors.Join(err, r.abandonContainer(ctx, spec.Ref, progress))
	}
	return result, nil
}

func (r *release) runContainer(ctx context.Context, spec provider.StackSpec, work *containerWork, progress progress.Log) (provider.StackResult, error) {
	var err error
	for attempt := range rulePlacements {
		if err = r.placeRule(ctx, work); err != nil {
			return provider.StackResult{}, err
		}
		spec.VendorState = work
		var result provider.StackResult
		if result, err = r.automation.Run(ctx, spec, progress); err == nil {
			return result, nil
		}
		if !priorityTaken(err) {
			return provider.StackResult{}, err
		}
		if progress != nil && attempt+1 < rulePlacements {
			progress.Say(fmt.Sprintf("Placing %s's listener rule again: another deploy claimed priority %d first (attempt %d of %d)", work.app, work.priority, attempt+2, rulePlacements))
		}
	}
	return provider.StackResult{}, fmt.Errorf("place %s's listener rule: every priority it picked was claimed by another deploy before it could take it, %d times over: %w", work.app, rulePlacements, err)
}

func readPublicRanges(spec provider.StackSpec) ([]string, error) {
	if !isRoutedPublicly(spec) {
		return nil, nil
	}
	var ranges []string
	if spec.Edge != nil {
		ranges = spec.Edge.Facts().OriginFacingRanges
	}
	if len(ranges) == 0 {
		return nil, refusal.Refuse(refusal.CodeInvalid,
			"app %s is forwarded to a public load balancer, and the edge in front of it names no addresses it forwards from, so the load balancer would answer the whole internet", spec.App.App)
	}
	return ranges, nil
}

func (r *release) abandonContainer(ctx context.Context, ref provider.StackRef, progress progress.Log) error {
	if err := r.automation.Destroy(ctx, ref, progress); err != nil {
		return err
	}
	return r.releaseContainerInfra(ctx, r.cfg.KeyValues, ref, progress)
}

func (r *release) planContainer(ctx context.Context, spec provider.StackSpec, progress progress.Log) (provider.Plan, error) {
	infra, present, err := r.readContainerInfra(ctx, spec.Ref.Tier)
	if err != nil {
		return provider.Plan{}, err
	}
	if !present || (isRoutedPublicly(spec) && infra.PublicListener == "") {
		shared := provider.ChangeGroup{
			Kind:   provider.StackGroupKind,
			Name:   containerInfraRef(spec.Ref.Tier).Name.String(),
			Action: provider.ActionCreate,
			Reason: "the first container deploy in the " + string(spec.Ref.Tier) + " tier provisions the load balancer and cluster every container app in it shares",
			Slow:   true,
		}
		if present {
			shared.Action = provider.ActionUpdate
			shared.Reason = "the first container app in the " + string(spec.Ref.Tier) + " tier an edge forwards to raises the public load balancer every such app shares"
		}
		return provider.Plan{Groups: []provider.ChangeGroup{
			shared,
			{
				Kind:   provider.StackGroupKind,
				Name:   spec.Ref.Name.String(),
				Action: provider.ActionCreate,
				Reason: provider.DetailUnavailable,
				Slow:   true,
			},
		}}, nil
	}
	work, err := r.containerWork(spec, infra)
	if err != nil {
		return provider.Plan{}, err
	}
	if work.transformed, err = transformStackSpec(ctx, r.cfg.Transform, spec); err != nil {
		return provider.Plan{}, err
	}
	if err := r.placeRule(ctx, work); err != nil {
		return provider.Plan{}, err
	}
	spec.VendorState = work
	previewed, err := r.automation.Preview(ctx, spec, progress)
	if err != nil {
		return provider.Plan{}, err
	}
	if err := work.transformed.refuseUnclaimed(); err != nil {
		return provider.Plan{}, err
	}
	return previewed, nil
}

func (r *release) decodeContainer(work *containerWork, outputs auto.OutputMap) (provider.StackResult, error) {
	raw, produced := outputs[work.app]
	if !produced {
		return provider.StackResult{}, fmt.Errorf("stack produced no output for %s", work.app)
	}
	fields, mapped := raw.Value.(map[string]any)
	if !mapped {
		return provider.StackResult{}, fmt.Errorf("output for %s is not a map", work.app)
	}
	url, err := requireStringField(fields, work.app, outputKeyContainerURL)
	if err != nil {
		return provider.StackResult{}, err
	}
	physical, err := requireStringField(fields, work.app, outputKeyContainerPhysical)
	if err != nil {
		return provider.StackResult{}, err
	}
	return provider.StackResult{Containers: []provider.AppContainer{{
		Name:     work.app,
		Physical: physical,
		URL:      url,
		Image:    work.image,
	}}}, nil
}
