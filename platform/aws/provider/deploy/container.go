package deploy

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/apprunner"
	iam "github.com/pulumi/pulumi-aws/sdk/v7/go/aws/iam"
	"github.com/pulumi/pulumi/sdk/v3/go/auto"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/providerkit"
)

const (
	containerPort   = "8080"
	containerCPU    = "1024"
	containerMemory = "2048"

	containerPortEnv            = "PORT"
	appRunnerReservedEnv        = "AWSAPPRUNNER"
	appRunnerTasksPrincipal     = "tasks.apprunner.amazonaws.com"
	appRunnerBuildPrincipal     = "build.apprunner.amazonaws.com"
	appRunnerECRAccessPolicyARN = "arn:aws:iam::aws:policy/service-role/AWSAppRunnerServicePolicyForECRAccess"

	containerLocalName = "container"
	pullRoleLocalName  = "pull"

	maxServiceNameLen = 40

	healthCheckIntervalSeconds = 10
	healthCheckTimeoutSeconds  = 5
	healthyThreshold           = 1
	unhealthyThreshold         = 5

	outputKeyContainerURL = "url"
	outputKeyContainerARN = "arn"
)

type containerWork struct {
	app        string
	image      string
	healthPath string
	env        map[string]string
	tags       map[string]string
	boundary   string
	policies   []linkPolicy
	service    naming.Coordinate
	pullRole   naming.Coordinate
	taskRole   naming.Coordinate
}

func (r *release) containerWork(plan providerkit.StackPlan) (*containerWork, error) {
	app := plan.App
	project, stack := naming.Sanitize(plan.Ref.Project), plan.Ref.Name
	if strings.TrimSpace(app.Image) == "" {
		return nil, providerkit.Refuse(providerkit.CodeInvalid,
			"app %s names no image, and a container on this provider runs what a registry coordinate names and nothing else", app.App)
	}
	if !providerkit.HealthCheckPath(app.HealthCheckPath) {
		return nil, providerkit.Refuse(providerkit.CodeInvalid,
			"app %s is probed at %q, which is not a path App Runner can send a health check to", app.App, app.HealthCheckPath)
	}
	if len(app.Functions) > 0 {
		return nil, providerkit.Refuse(providerkit.CodeInvalid,
			"app %s runs as a container and was packed into %d functions as well, so two things would answer the same request", app.App, len(app.Functions))
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
		policies:   policies,
		service:    serviceCoordinate(project, stack),
		pullRole:   containerRoleCoordinate(project, stack, pullRoleLocalName),
		taskRole:   containerRoleCoordinate(project, stack, roleLocalName),
	}, nil
}

func containerEnv(app string, values providerkit.AppValues) (map[string]string, error) {
	env := make(map[string]string, len(values.Delivered)+1)
	maps.Copy(env, values.Delivered)
	maps.Copy(env, values.Injected())
	for _, key := range slices.Sorted(maps.Keys(env)) {
		if key == containerPortEnv {
			return nil, providerkit.Refuse(providerkit.CodeInvalid,
				"app %s sets %s, and a container on this provider listens on %s, which App Runner hands it as %s: drop the value and read the port from the environment",
				app, containerPortEnv, containerPort, containerPortEnv)
		}
		if strings.HasPrefix(key, appRunnerReservedEnv) {
			return nil, providerkit.Refuse(providerkit.CodeInvalid,
				"app %s sets %s, and App Runner reserves every key starting with %s: rename it", app, key, appRunnerReservedEnv)
		}
	}
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

func containerRoleCoordinate(project string, stack naming.StackName, local string) naming.Coordinate {
	return naming.Coordinate{
		Project: project,
		Env:     stack.Env,
		App:     stack.App,
		Kind:    naming.KindRole,
		Name:    naming.Join(naming.WordSeparator, local, string(naming.KindRole)),
		Release: stack.Release,
	}
}

func (w *containerWork) run(ctx *pulumi.Context) error {
	pull, err := iam.NewRole(ctx, naming.ResourceID(naming.KindRole, pullRoleLocalName), &iam.RoleArgs{
		NamePrefix:          pulumi.String(rolePrefix(w.pullRole)),
		Description:         describe(w.pullRole, "role App Runner pulls this app's image with"),
		AssumeRolePolicy:    pulumi.String(assumeRolePolicy(appRunnerBuildPrincipal)),
		PermissionsBoundary: permissionsBoundary(w.boundary),
		Tags:                resourceTags(naming.KindRole, "", w.tags),
	})
	if err != nil {
		return err
	}
	if _, err := iam.NewRolePolicyAttachment(ctx, naming.ResourceID(naming.KindRole, pullRoleLocalName, "policy", "ecr"), &iam.RolePolicyAttachmentArgs{
		Role:      pull.Name,
		PolicyArn: pulumi.String(appRunnerECRAccessPolicyARN),
	}); err != nil {
		return err
	}

	instance := &apprunner.ServiceInstanceConfigurationArgs{
		Cpu:    pulumi.String(containerCPU),
		Memory: pulumi.String(containerMemory),
	}
	if len(w.policies) > 0 {
		task, err := w.taskRoleFor(ctx)
		if err != nil {
			return err
		}
		instance.InstanceRoleArn = task.Arn
	}

	env := pulumi.StringMap{}
	for key, value := range w.env {
		env[key] = pulumi.String(value)
	}
	service, err := apprunner.NewService(ctx, naming.ResourceID(naming.KindService, containerLocalName), &apprunner.ServiceArgs{
		ServiceName: pulumi.String(w.service.PhysicalName(maxServiceNameLen)),
		SourceConfiguration: &apprunner.ServiceSourceConfigurationArgs{
			AutoDeploymentsEnabled: pulumi.Bool(false),
			AuthenticationConfiguration: &apprunner.ServiceSourceConfigurationAuthenticationConfigurationArgs{
				AccessRoleArn: pull.Arn,
			},
			ImageRepository: &apprunner.ServiceSourceConfigurationImageRepositoryArgs{
				ImageIdentifier:     pulumi.String(w.image),
				ImageRepositoryType: pulumi.String("ECR"),
				ImageConfiguration: &apprunner.ServiceSourceConfigurationImageRepositoryImageConfigurationArgs{
					Port:                        pulumi.String(containerPort),
					RuntimeEnvironmentVariables: env,
				},
			},
		},
		HealthCheckConfiguration: &apprunner.ServiceHealthCheckConfigurationArgs{
			Protocol:           pulumi.String("HTTP"),
			Path:               pulumi.String(w.healthPath),
			Interval:           pulumi.Int(healthCheckIntervalSeconds),
			Timeout:            pulumi.Int(healthCheckTimeoutSeconds),
			HealthyThreshold:   pulumi.Int(healthyThreshold),
			UnhealthyThreshold: pulumi.Int(unhealthyThreshold),
		},
		InstanceConfiguration: instance,
		Tags:                  resourceTags(naming.KindService, "", w.tags),
	})
	if err != nil {
		return err
	}
	ctx.Export(w.app, pulumi.Map{
		outputKeyContainerURL: service.ServiceUrl.ApplyT(func(host string) string { return "https://" + host }).(pulumi.StringOutput),
		outputKeyContainerARN: service.Arn,
	})
	return nil
}

func (w *containerWork) taskRoleFor(ctx *pulumi.Context) (*iam.Role, error) {
	task, err := iam.NewRole(ctx, naming.ResourceID(naming.KindRole, roleLocalName), &iam.RoleArgs{
		NamePrefix:          pulumi.String(rolePrefix(w.taskRole)),
		Description:         describe(w.taskRole, "instance role for this app's container"),
		AssumeRolePolicy:    pulumi.String(assumeRolePolicy(appRunnerTasksPrincipal)),
		PermissionsBoundary: permissionsBoundary(w.boundary),
		Tags:                resourceTags(naming.KindRole, "", w.tags),
	})
	if err != nil {
		return nil, err
	}
	for _, link := range w.policies {
		if _, err := iam.NewRolePolicy(ctx, naming.ResourceID(naming.KindRole, roleLocalName, "policy", "link", link.Link), &iam.RolePolicyArgs{
			Role:   task.Name,
			Policy: pulumi.String(link.Policy),
		}); err != nil {
			return nil, err
		}
	}
	return task, nil
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
	arn, err := requireStringField(fields, work.app, outputKeyContainerARN)
	if err != nil {
		return providerkit.StackResult{}, err
	}
	return providerkit.StackResult{Containers: []providerkit.AppContainer{{
		Name:     work.app,
		Physical: arn,
		URL:      url,
		Image:    work.image,
	}}}, nil
}
