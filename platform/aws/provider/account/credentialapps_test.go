package account

import (
	"maps"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/platform/aws/provider/bastion"
	"github.com/ocelhq/ocel/platform/aws/provider/compute/container"
	"github.com/ocelhq/ocel/platform/aws/provider/iam"
	awsports "github.com/ocelhq/ocel/platform/aws/provider/ports"
	"github.com/ocelhq/ocel/platform/aws/provider/registry"
	"github.com/ocelhq/ocel/platform/aws/provider/tier/envsourcesync"
	"github.com/ocelhq/ocel/platform/aws/provider/tier/imageoptimizer"
	"github.com/ocelhq/ocel/platform/aws/provider/tier/taginvalidator"
)

func TestACredentialManagesOnlyTheFunctionsRolesAndLogGroupsOcelNames(t *testing.T) {
	const (
		functionARN = "arn:aws:lambda:us-east-1:111122223333:function:"
		logGroupARN = "arn:aws:logs:us-east-1:111122223333:log-group:/aws/lambda/"
		roleARN     = "arn:aws:iam::111122223333:role"
		appFunction = "ocel-app-shop-prod-web-r1a2b3c4d"
		stranger    = "payroll"
	)
	ns := string(defaultNamespace)
	for _, tier := range bothTiers {
		tierFunctions := []string{
			envsourcesync.NamesFor(ns, tier).Function,
			taginvalidator.NamesFor(ns, tier).Function,
			imageoptimizer.NamesFor(ns, tier).Function,
		}
		functions := append([]string{appFunction, awsports.PublicLivenessName(ns, tier)}, tierFunctions...)
		logGroups := append([]string{appFunction}, tierFunctions...)
		roles := append(partRolesOf(tier),
			roleARN+defaultNamespace.AppRolePathFor(tier)+"ocel-app-shop-prod-web-role-r1a2b3c4d",
			roleARN+defaultNamespace.AppRolePathFor(tier)+awsports.PublicLivenessName(ns, tier),
			roleARN+"/"+bastion.NameFor(ns, tier),
		)
		owned := appResourceOf(tier, nil)
		creating := requestContext{
			"aws:RequestTag/" + naming.ManagedByTagKey: ns,
			"aws:RequestTag/" + naming.EnvTierTagKey:   string(tier),
			"iam:PermissionsBoundary":                  "arn:aws:iam::111122223333:policy/" + defaultNamespace.AppBoundaryNameFor(tier),
			"aws:PrincipalAccount":                     "111122223333",
		}
		for purpose, document := range credentialsOfTier(t, tier) {
			for _, function := range functions {
				for _, action := range []string{"lambda:UpdateFunctionCode", "lambda:DeleteFunction"} {
					if !allows(t, document, action, functionARN+function, owned) {
						t.Errorf("the %s %s credential cannot %s on %s, a function Ocel makes", tier, purpose, action, function)
					}
				}
			}
			for _, group := range logGroups {
				if !allows(t, document, "logs:DeleteLogGroup", logGroupARN+group, owned) {
					t.Errorf("the %s %s credential cannot delete the log group of %s, a function Ocel makes", tier, purpose, group)
				}
			}
			for _, role := range roles {
				if !allows(t, document, "iam:DeleteRole", role, owned) {
					t.Errorf("the %s %s credential cannot delete %s, a role Ocel makes", tier, purpose, role)
				}
			}
			for _, c := range []struct{ action, arn string }{
				{"lambda:UpdateFunctionCode", functionARN + stranger},
				{"lambda:DeleteFunction", functionARN + stranger},
				{"logs:DeleteLogGroup", logGroupARN + stranger},
				{"logs:FilterLogEvents", logGroupARN + stranger},
				{"iam:DeleteRole", roleARN + "/" + stranger},
				{"iam:PutRolePolicy", roleARN + "/" + stranger},
			} {
				if allows(t, document, c.action, c.arn, owned) {
					t.Errorf("the %s %s credential can %s on %s, which carries Ocel's tags under a name Ocel never gives", tier, purpose, c.action, c.arn)
				}
			}
			for _, c := range []struct{ action, arn string }{
				{"lambda:CreateFunction", functionARN + stranger},
				{"logs:CreateLogGroup", logGroupARN + stranger},
				{"iam:CreateRole", roleARN + "/" + stranger},
			} {
				if allows(t, document, c.action, c.arn, creating) {
					t.Errorf("the %s %s credential can %s %s, a name Ocel never gives", tier, purpose, c.action, c.arn)
				}
			}
		}
	}
}

func TestTheDeployCredentialOwnsTheLogGroupsItCreates(t *testing.T) {
	grants := grantsOf(t, mustRender(t, DeployCredentialPermissions))
	want := map[string]string{
		"logs:CreateLogGroup":      conditionJSON(t, productionScope.TaggedOnCreateForItsTier()),
		"logs:DeleteLogGroup":      conditionJSON(t, productionScope.TaggedByOcelForItsTier()),
		"logs:FilterLogEvents":     conditionJSON(t, productionScope.TaggedByOcelForItsTier()),
		"logs:ListTagsForResource": conditionJSON(t, productionScope.TaggedByOcelForItsTier()),
		"logs:PutRetentionPolicy":  conditionJSON(t, productionScope.TaggedByOcelForItsTier()),
		"logs:StartLiveTail":       conditionJSON(t, productionScope.TaggedByOcelForItsTier()),
		"logs:TagResource":         conditionJSON(t, productionScope.TaggedByOcelForItsTier()),
		"logs:UntagResource":       conditionJSON(t, productionScope.TaggedByOcelForItsTier()),
	}
	for _, resource := range append(productionScope.ContainerLogGroupARNs(), productionScope.FunctionLogGroupARNs()...) {
		got := logsGrantsOn(grants, resource)
		if !maps.Equal(got, want) {
			t.Errorf("the deploy credential grants %v on %s, want exactly %v, so a log group is either never made with a retention, never reclaimed by the teardown, or reachable beyond what ocel tagged", got, resource, want)
		}
	}
}

func TestTheDeployCredentialIsGrantedGetFunctionConfiguration(t *testing.T) {
	grants := grantsOf(t, mustRender(t, DeployCredentialPermissions))
	for g := range grants {
		if g.action == "lambda:GetFunctionConfiguration" {
			return
		}
	}
	t.Error("the deploy credential does not grant lambda:GetFunctionConfiguration, so a log read cannot learn which log group a function writes to")
}

func TestEveryCredentialListsLogGroupsOnTheOnlyResourceAWSAccepts(t *testing.T) {
	for purpose, document := range bothCredentials(t) {
		grants := grantsOf(t, document)
		if !grants[grant{action: "logs:DescribeLogGroups", resource: iam.AnyResource, condition: conditionJSON(t, nil)}] {
			t.Errorf("the %s credential does not grant logs:DescribeLogGroups on %q, the only resource IAM evaluates it against, so CloudFormation cannot read back a log group it manages", purpose, iam.AnyResource)
		}
		for g := range grants {
			if g.action == "logs:DescribeLogGroups" && g.resource != iam.AnyResource {
				t.Errorf("the %s credential grants logs:DescribeLogGroups on %s, an ARN IAM never matches for an action with no resource type", purpose, g.resource)
			}
		}
	}
}

func TestACredentialManagesOnlyTheTargetGroupsAndTaskFamiliesOcelNames(t *testing.T) {
	const (
		targetGroupARN    = "arn:aws:elasticloadbalancing:us-east-1:111122223333:targetgroup/"
		taskDefinitionARN = "arn:aws:ecs:us-east-1:111122223333:task-definition/"
		stranger          = "payroll"
	)
	ns := string(defaultNamespace)
	release, err := naming.ParseReleaseToken("r1a2b3c4d")
	if err != nil {
		t.Fatal(err)
	}
	stack := naming.AppStack("prod", "web", release)
	service, err := container.NamesFor(ns, "shop", stack)
	if err != nil {
		t.Fatal(err)
	}
	worker, err := container.WorkerServiceName(ns, "shop", "prod", "web", "mailer")
	if err != nil {
		t.Fatal(err)
	}
	for _, tier := range bothTiers {
		targetGroups := []string{service.TargetGroup, awsports.PublicTargetGroupName(ns, tier)}
		families := []string{service.Service, worker, bastion.NameFor(ns, tier)}
		owned := appResourceOf(tier, nil)
		creating := requestContext{
			"aws:RequestTag/" + naming.ManagedByTagKey: ns,
			"aws:RequestTag/" + naming.EnvTierTagKey:   string(tier),
		}
		_, document := renderedCredentialsOf(t, tier)
		for _, group := range targetGroups {
			arn := targetGroupARN + group + "/0123456789abcdef"
			if !allows(t, document, "elasticloadbalancing:CreateTargetGroup", arn, creating) {
				t.Errorf("the %s deploy credential cannot create %s, a target group Ocel makes", tier, group)
			}
			for _, action := range []string{"elasticloadbalancing:ModifyTargetGroup", "elasticloadbalancing:DeleteTargetGroup"} {
				if !allows(t, document, action, arn, owned) {
					t.Errorf("the %s deploy credential cannot %s on %s, a target group Ocel makes", tier, action, group)
				}
			}
		}
		for _, family := range families {
			if !allows(t, document, "ecs:RegisterTaskDefinition", taskDefinitionARN+family+":1", creating) {
				t.Errorf("the %s deploy credential cannot register %s, a task family Ocel makes", tier, family)
			}
		}
		for _, c := range []struct {
			action, arn string
			request     requestContext
		}{
			{"elasticloadbalancing:CreateTargetGroup", targetGroupARN + stranger + "/0123456789abcdef", creating},
			{"elasticloadbalancing:ModifyTargetGroup", targetGroupARN + stranger + "/0123456789abcdef", owned},
			{"elasticloadbalancing:DeleteTargetGroup", targetGroupARN + stranger + "/0123456789abcdef", owned},
			{"elasticloadbalancing:AddTags", targetGroupARN + stranger + "/0123456789abcdef", creating},
			{"ecs:RegisterTaskDefinition", taskDefinitionARN + stranger + ":1", creating},
		} {
			if allows(t, document, c.action, c.arn, c.request) {
				t.Errorf("the %s deploy credential can %s on %s, which carries Ocel's tags under a name Ocel never gives", tier, c.action, c.arn)
			}
		}
	}
}

func TestEveryCredentialScopesTaskDefinitionsToWhatAWSEvaluates(t *testing.T) {
	unscopable := []string{"ecs:DeregisterTaskDefinition", "ecs:DescribeTaskDefinition", "ecs:ListTaskDefinitions"}
	for purpose, document := range bothCredentials(t) {
		grants := grantsOf(t, document)
		scope := defaultNamespace.Scope(tierOf(purpose))
		registered := []string{scope.AppTaskDefinitionARN(), bastion.TaskDefinitionARN(scope.Namespace, scope.Tier)}
		for _, family := range registered {
			if !grants[grant{action: "ecs:RegisterTaskDefinition", resource: family, condition: conditionJSON(t, scope.TaggedOnCreateForItsTier())}] {
				t.Errorf("the %s credential does not grant ecs:RegisterTaskDefinition on %s under a create tag, so a container release either cannot register its family or registers one nothing marks as Ocel's", purpose, family)
			}
		}
		for _, action := range unscopable {
			if !grants[grant{action: action, resource: iam.AnyResource, condition: conditionJSON(t, nil)}] {
				t.Errorf("the %s credential does not grant %s on %q, the only resource IAM evaluates it against, so a container release cannot read back or reclaim its task definition", purpose, action, iam.AnyResource)
			}
		}
		for g := range grants {
			if g.action == "ecs:RegisterTaskDefinition" && !slices.Contains(registered, g.resource) {
				t.Errorf("the %s credential grants ecs:RegisterTaskDefinition on %s, which reaches past the task definitions a deploy registers", purpose, g.resource)
			}
			if slices.Contains(unscopable, g.action) && g.resource != iam.AnyResource {
				t.Errorf("the %s credential grants %s on %s, an ARN IAM never matches for an action with no resource type", purpose, g.action, g.resource)
			}
		}
	}
}

func logsGrantsOn(grants map[grant]bool, resource string) map[string]string {
	got := map[string]string{}
	for g := range grants {
		if g.resource == resource && strings.HasPrefix(g.action, "logs:") {
			got[g.action] = g.condition
		}
	}
	return got
}

func TestTheBootstrapCredentialReachesNoLogGroupBeyondWhatADeployMay(t *testing.T) {
	bootstrapDoc, deployDoc := renderedCredentials(t)
	bootstrapGrants, deployGrants := grantsOf(t, bootstrapDoc), grantsOf(t, deployDoc)
	owned := append(productionScope.ContainerLogGroupARNs(), productionScope.FunctionLogGroupARNs()...)
	for _, resource := range owned {
		if got, deploy := logsGrantsOn(bootstrapGrants, resource), logsGrantsOn(deployGrants, resource); !maps.Equal(got, deploy) {
			t.Errorf("the bootstrap credential grants %v on %s, want the deploy credential's %v, so bootstrapping widens what a deploy may do to a log group", got, resource, deploy)
		}
	}
	for g := range bootstrapGrants {
		if !strings.HasPrefix(g.action, "logs:") {
			continue
		}
		switch {
		case slices.Contains(owned, g.resource):
		case g.resource == iam.AnyResource:
			if g.action != "logs:DescribeLogGroups" {
				t.Errorf("the bootstrap credential grants %s on %q, which reaches every log group in the account", g.action, g.resource)
			}
		default:
			t.Errorf("the bootstrap credential grants %s on %s, beyond the log groups a deploy owns: no bootstrap stack declares a function", g.action, g.resource)
		}
	}
}

func TestEveryCredentialTouchesOnlyEventSourceMappingsOfItsTagInvalidatorOrOcelTaggedOnesOfAppWorkers(t *testing.T) {
	ofAppWorkers := map[string]any{"lambda:FunctionArn": productionScope.AppFunctionARN()}
	for _, tier := range bothTiers {
		invalidator := "arn:aws:lambda:*:*:function:" + taginvalidator.NamesFor(string(defaultNamespace), tier).Function
		allowed := map[string]bool{
			conditionJSON(t, map[string]any{"ArnLike": map[string]any{"lambda:FunctionArn": []string{invalidator, invalidator + ":*"}}}): true,
			conditionJSON(t, map[string]any{
				"StringEquals": map[string]any{"aws:RequestTag/ocel:managed-by": "ocel", "aws:RequestTag/ocel:env-tier": string(tier)},
				"ArnLike":      ofAppWorkers,
			}): true,
			conditionJSON(t, map[string]any{
				"StringEquals": map[string]any{"aws:ResourceTag/ocel:managed-by": "ocel", "aws:ResourceTag/ocel:env-tier": string(tier)},
				"ArnLike":      ofAppWorkers,
			}): true,
		}
		for purpose, document := range credentialsOfTier(t, tier) {
			for g := range grantsOf(t, document) {
				if !strings.HasSuffix(g.action, "EventSourceMapping") {
					continue
				}
				if !allowed[g.condition] {
					t.Errorf("the %s %s credential grants %s on %s under %s, want it pinned to its own tier's tag invalidator or its own tier's Ocel-tagged mappings of app worker functions", tier, purpose, g.action, g.resource, g.condition)
				}
			}
		}
	}
}

func TestTheDeployCredentialMakesAndRemovesOnlyTheQueuesTopicsAndSchedulesOfApps(t *testing.T) {
	scoped := map[string][]string{
		"sqs:CreateQueue":               {productionScope.AppQueueARN(), productionScope.TagInvalidatorQueue},
		"sqs:DeleteQueue":               {productionScope.AppQueueARN(), productionScope.TagInvalidatorQueue},
		"sns:CreateTopic":               {productionScope.AppTopicARN(), productionScope.AppSubscriptionARN()},
		"sns:DeleteTopic":               {productionScope.AppTopicARN(), productionScope.AppSubscriptionARN()},
		"scheduler:CreateSchedule":      {productionScope.AppScheduleARN(), productionScope.EnvSourceSyncSchedule},
		"scheduler:DeleteSchedule":      {productionScope.AppScheduleARN(), productionScope.EnvSourceSyncSchedule},
		"scheduler:CreateScheduleGroup": {productionScope.AppScheduleGroupARN(), productionScope.EnvSourceSyncScheduleGroup},
		"scheduler:DeleteScheduleGroup": {productionScope.AppScheduleGroupARN(), productionScope.EnvSourceSyncScheduleGroup},
	}
	granted := map[string]bool{}
	for g := range grantsOf(t, mustRender(t, DeployCredentialPermissions)) {
		resources, watched := scoped[g.action]
		if !watched {
			continue
		}
		granted[g.action] = true
		if !slices.Contains(resources, g.resource) {
			t.Errorf("the deploy credential grants %s on %s, want it only on %v, the app scope a deploy names its queues, topics and schedules under", g.action, g.resource, resources)
		}
	}
	for action := range scoped {
		if !granted[action] {
			t.Errorf("the deploy credential grants no %s, and a deploy of a task, topic or cron task needs it", action)
		}
	}
}

func TestTheDeployCredentialScalesOnlyTheECSServicesItTagged(t *testing.T) {
	grants := grantsOf(t, mustRender(t, DeployCredentialPermissions))
	onCreate := conditionJSON(t, iam.ScalesECSServices(productionScope.TaggedOnCreateForItsTier()))
	tagged := conditionJSON(t, iam.ScalesECSServices(productionScope.TaggedByOcelForItsTier()))
	want := []grant{
		{action: "application-autoscaling:RegisterScalableTarget", resource: iam.ScalableTargetARN, condition: onCreate},
		{action: "application-autoscaling:RegisterScalableTarget", resource: iam.ScalableTargetARN, condition: tagged},
		{action: "application-autoscaling:DeregisterScalableTarget", resource: iam.ScalableTargetARN, condition: tagged},
		{action: "application-autoscaling:PutScalingPolicy", resource: iam.ScalableTargetARN, condition: tagged},
		{action: "application-autoscaling:DeleteScalingPolicy", resource: iam.ScalableTargetARN, condition: tagged},
		{action: "application-autoscaling:TagResource", resource: iam.ScalableTargetARN, condition: conditionJSON(t, productionScope.TaggedOnCreateForItsTier())},
		{action: "application-autoscaling:TagResource", resource: iam.ScalableTargetARN, condition: conditionJSON(t, productionScope.TaggedByOcelForItsTier())},
		{action: "application-autoscaling:UntagResource", resource: iam.ScalableTargetARN, condition: conditionJSON(t, productionScope.TaggedByOcelForItsTier())},
		{action: "cloudwatch:PutMetricAlarm", resource: productionScope.ScalingAlarmARN(), condition: "null"},
		{action: "cloudwatch:DeleteAlarms", resource: productionScope.ScalingAlarmARN(), condition: "null"},
		{action: "iam:CreateServiceLinkedRole", resource: iam.ECSScalingLinkedRoleARN, condition: conditionJSON(t, iam.LinkedRoleFor("ecs.application-autoscaling.amazonaws.com"))},
	}
	for _, wanted := range want {
		if !grants[wanted] {
			t.Errorf("the deploy credential does not grant %s on %s under %s: a container app that scales between its instance counts needs its service registered and a request policy put on it", wanted.action, wanted.resource, wanted.condition)
		}
	}
}

type ecrRequest struct {
	action           string
	principalProject string
	repository       string
	foreign          bool
	requestProject   string
	resourceProject  string
}

func (r ecrRequest) arn(tier environment.Tier) string {
	repository := registry.RepositoryPrefix(string(defaultNamespace), tier) + "/" + r.repository
	if r.foreign {
		repository = r.repository
	}
	return "arn:aws:ecr:us-east-1:123456789012:repository/" + repository
}

func (r ecrRequest) context() requestContext {
	context := requestContext{
		"aws:PrincipalAccount": "123456789012",
		"aws:ResourceAccount":  "123456789012",
	}
	if r.principalProject != "" {
		context["aws:PrincipalTag/ocel:project"] = r.principalProject
	}
	if r.requestProject != "" {
		context["aws:RequestTag/ocel:project"] = r.requestProject
		context["aws:RequestTag/ocel:managed-by"] = "ocel"
	}
	if r.resourceProject != "" {
		context["aws:ResourceTag/ocel:project"] = r.resourceProject
		context["aws:ResourceTag/ocel:managed-by"] = "ocel"
	}
	return context
}

var policyVariable = regexp.MustCompile(`\$\{([^}]*)\}`)

func resolveVariables(t *testing.T, value string, context map[string]string) (string, bool) {
	t.Helper()
	resolved := true
	out := policyVariable.ReplaceAllStringFunc(value, func(variable string) string {
		key := policyVariable.FindStringSubmatch(variable)[1]
		if strings.Contains(key, ",") {
			t.Fatalf("%s gives a policy variable a default, whose matching IAM documents nowhere: name what each principal may reach in statements of its own", value)
		}
		got, ok := context[key]
		resolved = resolved && ok
		return got
	})
	return out, resolved
}

func TestEveryCredentialReachesImageRepositoriesOnlyOfTheProjectItsPrincipalIsTaggedWith(t *testing.T) {
	cases := []struct {
		name    string
		request ecrRequest
		allowed bool
	}{
		{"an untagged principal deletes in an Ocel repository tagged with a project", ecrRequest{action: "ecr:BatchDeleteImage", repository: "shop.web", resourceProject: "shop"}, true},
		{"an untagged principal removes an Ocel repository tagged with a project", ecrRequest{action: "ecr:DeleteRepository", repository: "shop.web", resourceProject: "shop"}, true},
		{"an untagged principal deletes in a repository no project is tagged on", ecrRequest{action: "ecr:BatchDeleteImage", repository: "shop.web"}, false},
		{"an untagged principal creates a repository tagged with its project", ecrRequest{action: "ecr:CreateRepository", repository: "shop.web", requestProject: "shop"}, true},
		{"an untagged principal creates a repository with no project tag", ecrRequest{action: "ecr:CreateRepository", repository: "shop.web"}, false},
		{"an untagged principal tags a repository made before the tag existed", ecrRequest{action: "ecr:TagResource", repository: "shop.web", requestProject: "shop"}, true},
		{"a tagged principal deletes in its project's repository", ecrRequest{action: "ecr:BatchDeleteImage", principalProject: "shop", repository: "shop.web", resourceProject: "shop"}, true},
		{"a tagged principal removes its project's repository", ecrRequest{action: "ecr:DeleteRepository", principalProject: "shop", repository: "shop.web", resourceProject: "shop"}, true},
		{"a tagged principal deletes in another project's repository", ecrRequest{action: "ecr:BatchDeleteImage", principalProject: "shop", repository: "blog.web", resourceProject: "blog"}, false},
		{"a tagged principal deletes in another project's repository it tagged as its own", ecrRequest{action: "ecr:BatchDeleteImage", principalProject: "shop", repository: "blog.web", resourceProject: "shop"}, false},
		{"a tagged principal deletes in its project's repository tagged with another project", ecrRequest{action: "ecr:BatchDeleteImage", principalProject: "shop", repository: "shop.web", resourceProject: "blog"}, false},
		{"a tagged principal creates its project's repository", ecrRequest{action: "ecr:CreateRepository", principalProject: "shop", repository: "shop.web", requestProject: "shop"}, true},
		{"a tagged principal creates another project's repository tagged as its own", ecrRequest{action: "ecr:CreateRepository", principalProject: "shop", repository: "blog.web", requestProject: "shop"}, false},
		{"a tagged principal creates its project's repository tagged with another project", ecrRequest{action: "ecr:CreateRepository", principalProject: "shop", repository: "shop.web", requestProject: "blog"}, false},
		{"a tagged principal creates a repository whose project only starts with its own", ecrRequest{action: "ecr:CreateRepository", principalProject: "shop", repository: "shop-two.web", requestProject: "shop"}, false},
		{"a tagged principal tags its project's repository", ecrRequest{action: "ecr:TagResource", principalProject: "shop", repository: "shop.web", requestProject: "shop"}, true},
		{"a tagged principal tags another project's repository as its own", ecrRequest{action: "ecr:TagResource", principalProject: "shop", repository: "blog.web", requestProject: "shop", resourceProject: "blog"}, false},
		{"a tagged principal pushes to its project's repository", ecrRequest{action: "ecr:PutImage", principalProject: "shop", repository: "shop.web", resourceProject: "shop"}, true},
		{"any principal deletes outside the ocel namespace", ecrRequest{action: "ecr:BatchDeleteImage", repository: "theirs/web", foreign: true, resourceProject: "shop"}, false},
	}
	for purpose, document := range bothCredentials(t) {
		for _, c := range cases {
			if got := allows(t, document, c.request.action, c.request.arn(tierOf(purpose)), c.request.context()); got != c.allowed {
				t.Errorf("the %s credential: %s is allowed = %t, want %t", purpose, c.name, got, c.allowed)
			}
		}
	}
}

func bastionScopesOf(tier environment.Tier) (cluster, tasks, definitions string) {
	ns := string(defaultNamespace)
	return bastion.ClusterARN(ns, tier), bastion.TaskARN(ns, tier), bastion.TaskDefinitionARN(ns, tier)
}

func onBastionOf(t *testing.T, tier environment.Tier) string {
	cluster, _, _ := bastionScopesOf(tier)
	return conditionJSON(t, map[string]any{"ArnEquals": map[string]any{"ecs:cluster": cluster}})
}

func TestEveryCredentialRunsAndReachesOnlyTheTasksOfItsTiersBastionCluster(t *testing.T) {
	for purpose, document := range bothCredentials(t) {
		cluster, tasks, definitions := bastionScopesOf(tierOf(purpose))
		onBastion := onBastionOf(t, tierOf(purpose))
		want := []grant{
			{action: "ecs:RunTask", resource: definitions, condition: onBastion},
			{action: "ecs:DescribeTasks", resource: tasks, condition: "null"},
			{action: "ecs:StopTask", resource: tasks, condition: "null"},
			{action: "ecs:ExecuteCommand", resource: cluster, condition: "null"},
			{action: "ecs:ExecuteCommand", resource: tasks, condition: "null"},
			{action: "ecs:ListTasks", resource: iam.AnyResource, condition: onBastion},
			{action: "ecs:DescribeClusters", resource: cluster, condition: "null"},
			{action: "ecs:DeleteTaskDefinitions", resource: definitions, condition: "null"},
			{action: "ssm:StartSession", resource: tasks, condition: "null"},
			{action: "ssm:StartSession", resource: bastion.PortForwardingDocumentARN, condition: "null"},
		}
		grants := grantsOf(t, document)
		for _, wanted := range want {
			if !grants[wanted] {
				t.Errorf("the %s credential does not grant %s on %s under %s: the bastion a build forwards ports through cannot be run and reached without it", purpose, wanted.action, wanted.resource, wanted.condition)
			}
		}
	}
}

func TestNoCredentialRunsOrReachesAnythingBeyondTheBastionScopes(t *testing.T) {
	reachable := []string{"ecs:RunTask", "ecs:StopTask", "ecs:ExecuteCommand", "ssm:StartSession", "ssm:TerminateSession"}
	for purpose, document := range bothCredentials(t) {
		cluster, tasks, definitions := bastionScopesOf(tierOf(purpose))
		scopes := []string{cluster, tasks, definitions, bastion.PortForwardingDocumentARN}
		for g := range grantsOf(t, document) {
			if slices.Contains(reachable, g.action) && !slices.Contains(scopes, g.resource) {
				t.Errorf("the %s credential grants %s on %s, which reaches past the bastion cluster's tasks and the one port forwarding document", purpose, g.action, g.resource)
			}
		}
	}
}

func TestEveryCredentialListsTasksOnlyOfABastionClusterAndAgainstTheResourceAWSEvaluates(t *testing.T) {
	for purpose, document := range bothCredentials(t) {
		onBastion := onBastionOf(t, tierOf(purpose))
		for g := range grantsOf(t, document) {
			if g.action != "ecs:ListTasks" {
				continue
			}
			if g.resource != iam.AnyResource || g.condition != onBastion {
				t.Errorf("the %s credential grants ecs:ListTasks on %s under %s, want it on %s under ecs:cluster of a bastion cluster: AWS evaluates ListTasks against a container instance, so a cluster ARN never matches", purpose, g.resource, g.condition, iam.AnyResource)
			}
		}
	}
}

func TestNoCredentialTerminatesSessionsItHasNoCallFor(t *testing.T) {
	for purpose, document := range bothCredentials(t) {
		if actionsOf(t, document)["ssm:TerminateSession"] {
			t.Errorf("the %s credential grants ssm:TerminateSession, which no port forward calls: a session ends with the TerminateSession flag on its own data channel", purpose)
		}
	}
}

func TestEveryCredentialMintsAppRolesUnderOnlyTheBoundaryOfItsOwnTier(t *testing.T) {
	for _, tier := range bothTiers {
		for purpose, document := range credentialsOfTier(t, tier) {
			pinned := false
			for _, statement := range parsePolicy(t, document).Statement {
				if !slices.Contains(stringsOf(t, statement.Action, "Action"), "iam:CreateRole") || !boundaryScopes(statement.Condition) {
					continue
				}
				pinned = true
				operands, _ := statement.Condition["StringEquals"].(map[string]any)
				if got, want := operands["iam:PermissionsBoundary"], appBoundaryARNFor(defaultNamespace, tier); got != want {
					t.Errorf("the %s %s credential mints roles under iam:PermissionsBoundary %v, want exactly %s", tier, purpose, got, want)
				}
			}
			if !pinned {
				t.Errorf("the %s %s credential mints app roles without pinning a boundary", tier, purpose)
			}
		}
	}
}

func appResourceOf(tier environment.Tier, also requestContext) requestContext {
	request := taggedWithTier(tier, requestContext{"aws:ResourceTag/" + naming.ManagedByTagKey: string(defaultNamespace)})
	maps.Copy(request, also)
	return request
}

func TestACredentialReachesTheAppFunctionsAndRolesOfOnlyItsOwnTier(t *testing.T) {
	const (
		function = "arn:aws:lambda:us-east-1:111122223333:function:ocel-app-shop-pr-7-web-a1b2c3"
		mapping  = "arn:aws:lambda:us-east-1:111122223333:event-source-mapping:0a1b2c3d-4e5f-6789-abcd-ef0123456789"
	)
	roleOf := func(tier environment.Tier) string {
		return "arn:aws:iam::111122223333:role" + defaultNamespace.AppRolePathFor(tier) + "ocel-app-shop-pr-7-web-role-a1b2c3"
	}
	worker := requestContext{"lambda:FunctionArn": "arn:aws:lambda:us-east-1:111122223333:function:ocel-app-shop-pr-7-worker-a1b2c3"}
	for _, tier := range bothTiers {
		sibling := tier.Sibling()
		role, siblingRole := roleOf(tier), roleOf(sibling)
		for purpose, document := range credentialsOfTier(t, tier) {
			for _, c := range []struct {
				action, arn, siblingARN string
				also                    requestContext
			}{
				{"lambda:UpdateFunctionCode", function, function, nil},
				{"lambda:UpdateFunctionConfiguration", function, function, nil},
				{"lambda:InvokeFunction", function, function, nil},
				{"lambda:AddPermission", function, function, nil},
				{"lambda:DeleteFunction", function, function, nil},
				{"lambda:TagResource", function, function, nil},
				{"iam:PassRole", role, siblingRole, requestContext{"iam:PassedToService": iam.LambdaPrincipal}},
				{"iam:PassRole", role, siblingRole, requestContext{"iam:PassedToService": iam.ECSTasksPrincipal}},
				{"iam:PassRole", role, siblingRole, requestContext{"iam:PassedToService": iam.SchedulerPrincipal}},
				{"iam:DeleteRole", role, siblingRole, nil},
				{"iam:UpdateRole", role, siblingRole, nil},
				{"iam:TagRole", role, siblingRole, nil},
				{"lambda:UpdateEventSourceMapping", mapping, mapping, worker},
				{"lambda:DeleteEventSourceMapping", mapping, mapping, worker},
				{"lambda:TagResource", mapping, mapping, nil},
			} {
				if !allows(t, document, c.action, c.arn, appResourceOf(tier, c.also)) {
					t.Errorf("the %s %s credential cannot %s on %s, which its own tier's deploys made", tier, purpose, c.action, c.arn)
				}
				if allows(t, document, c.action, c.siblingARN, appResourceOf(sibling, c.also)) {
					t.Errorf("the %s %s credential can %s on %s, which the %s tier's deploys made", tier, purpose, c.action, c.siblingARN, sibling)
				}
			}
			creating := func(of environment.Tier, also requestContext) requestContext {
				request := requestContext{
					"aws:RequestTag/" + naming.ManagedByTagKey: string(defaultNamespace),
					"aws:RequestTag/" + naming.EnvTierTagKey:   string(of),
				}
				maps.Copy(request, also)
				return request
			}
			for _, c := range []struct {
				action, arn string
				also        requestContext
			}{
				{"lambda:CreateFunction", function, nil},
				{"iam:CreateRole", role, requestContext{
					"aws:PrincipalAccount":    "111122223333",
					"iam:PermissionsBoundary": "arn:aws:iam::111122223333:policy/" + defaultNamespace.AppBoundaryNameFor(tier),
				}},
				{"lambda:CreateEventSourceMapping", iam.AnyResource, worker},
			} {
				if !allows(t, document, c.action, c.arn, creating(tier, c.also)) {
					t.Errorf("the %s %s credential cannot %s tagged for its own tier", tier, purpose, c.action)
				}
				if allows(t, document, c.action, c.arn, creating(sibling, c.also)) {
					t.Errorf("the %s %s credential can %s tagged for the %s tier", tier, purpose, c.action, sibling)
				}
			}
			retag := appResourceOf(tier, requestContext{"aws:RequestTag/" + naming.EnvTierTagKey: string(sibling)})
			for _, c := range []struct{ action, arn string }{{"lambda:TagResource", function}, {"iam:TagRole", role}, {"lambda:TagResource", mapping}} {
				if allows(t, document, c.action, c.arn, retag) {
					t.Errorf("the %s %s credential can %s on %s as the %s tier's", tier, purpose, c.action, c.arn, sibling)
				}
			}
		}
		bootstrapDoc, _ := renderedCredentialsOf(t, tier)
		if !allows(t, bootstrapDoc, "iam:DeleteRolePermissionsBoundary", role, appResourceOf(tier, nil)) {
			t.Errorf("the %s bootstrap credential cannot lift the boundary of its own tier's app role", tier)
		}
		if allows(t, bootstrapDoc, "iam:DeleteRolePermissionsBoundary", siblingRole, appResourceOf(sibling, nil)) {
			t.Errorf("the %s bootstrap credential can lift the boundary of the %s tier's app role", tier, sibling)
		}
	}
}

func TestACredentialPassesTheBastionTaskRoleOfOnlyItsOwnTierToECS(t *testing.T) {
	toTasks := requestContext{"iam:PassedToService": iam.ECSTasksPrincipal}
	roleOf := func(tier environment.Tier) string {
		return "arn:aws:iam::111122223333:role/" + bastion.NameFor(string(defaultNamespace), tier)
	}
	for _, tier := range bothTiers {
		for purpose, document := range credentialsOfTier(t, tier) {
			if !allows(t, document, "iam:PassRole", roleOf(tier), toTasks) {
				t.Errorf("the %s %s credential cannot pass %s to ECS, so its bastion task cannot be registered", tier, purpose, roleOf(tier))
			}
			if allows(t, document, "iam:PassRole", roleOf(tier.Sibling()), toTasks) {
				t.Errorf("the %s %s credential can pass %s, the %s tier's bastion role, to ECS", tier, purpose, roleOf(tier.Sibling()), tier.Sibling())
			}
		}
	}
}

func TestTheDeployCredentialRaisesAndRemovesOnlyItsOwnTiersBacklogAlarmsUnderTheAppScope(t *testing.T) {
	grants := grantsOf(t, mustRender(t, DeployCredentialPermissions))
	ofItsTier := conditionJSON(t, productionScope.TaggedOnlyAsItsTier())
	byOcel := conditionJSON(t, productionScope.TaggedByOcelForItsTier())
	for action, condition := range map[string]string{
		"cloudwatch:PutMetricAlarm":      ofItsTier,
		"cloudwatch:TagResource":         ofItsTier,
		"cloudwatch:DeleteAlarms":        byOcel,
		"cloudwatch:UntagResource":       byOcel,
		"cloudwatch:ListTagsForResource": byOcel,
	} {
		if wanted := (grant{action: action, resource: productionScope.AppAlarmARN(), condition: condition}); !grants[wanted] {
			t.Errorf("the deploy credential does not grant %s on %s under %s: a container worker at no tasks is started by an alarm on its queues, and the other tier's alarms are not its own", action, productionScope.AppAlarmARN(), condition)
		}
	}
	for g := range grants {
		if g.action == "cloudwatch:PutMetricAlarm" && g.resource != productionScope.AppAlarmARN() && g.resource != productionScope.ScalingAlarmARN() {
			t.Errorf("the deploy credential grants cloudwatch:PutMetricAlarm on %s, want only the alarms Ocel names and the ones target tracking makes", g.resource)
		}
	}
}

func TestTheDeployCredentialBuildsAndTearsDownTheFunctionNetworkOnlyWhereOcelTaggedIt(t *testing.T) {
	grants := grantsOf(t, mustRender(t, DeployCredentialPermissions))
	onCreate := conditionJSON(t, productionScope.TaggedOnCreateForItsTier())
	byOcel := conditionJSON(t, productionScope.TaggedByOcelForItsTier())
	unscoped := conditionJSON(t, nil)

	creates := map[string][]string{
		"ec2:CreateSubnet":      {"arn:aws:ec2:*:*:subnet/*", "arn:aws:ec2:*:*:vpc/*"},
		"ec2:AllocateAddress":   {"arn:aws:ec2:*:*:elastic-ip/*"},
		"ec2:CreateNatGateway":  {"arn:aws:ec2:*:*:natgateway/*", "arn:aws:ec2:*:*:subnet/*", "arn:aws:ec2:*:*:elastic-ip/*"},
		"ec2:CreateRouteTable":  {"arn:aws:ec2:*:*:route-table/*", "arn:aws:ec2:*:*:vpc/*"},
		"ec2:CreateVpcEndpoint": {"arn:aws:ec2:*:*:vpc-endpoint/*", "arn:aws:ec2:*:*:vpc/*", "arn:aws:ec2:*:*:route-table/*"},
	}
	for action, resources := range creates {
		for _, resource := range resources {
			if !grants[grant{action: action, resource: resource, condition: onCreate}] {
				t.Errorf("the deploy credential does not grant %s on %s with Ocel's tag required on create, so the function network cannot be built or is built untagged", action, resource)
			}
		}
	}

	mutations := map[string][]string{
		"ec2:DeleteSubnet":           {"arn:aws:ec2:*:*:subnet/*"},
		"ec2:ReleaseAddress":         {"arn:aws:ec2:*:*:elastic-ip/*"},
		"ec2:DeleteNatGateway":       {"arn:aws:ec2:*:*:natgateway/*"},
		"ec2:DeleteRouteTable":       {"arn:aws:ec2:*:*:route-table/*"},
		"ec2:CreateRoute":            {"arn:aws:ec2:*:*:route-table/*"},
		"ec2:DeleteRoute":            {"arn:aws:ec2:*:*:route-table/*"},
		"ec2:AssociateRouteTable":    {"arn:aws:ec2:*:*:route-table/*", "arn:aws:ec2:*:*:subnet/*"},
		"ec2:DisassociateRouteTable": {"arn:aws:ec2:*:*:route-table/*", "arn:aws:ec2:*:*:subnet/*"},
		"ec2:DeleteVpcEndpoints":     {"arn:aws:ec2:*:*:vpc-endpoint/*"},
		"ec2:ModifyVpcEndpoint":      {"arn:aws:ec2:*:*:vpc-endpoint/*", "arn:aws:ec2:*:*:route-table/*"},
	}
	for action, resources := range mutations {
		for _, resource := range resources {
			if !grants[grant{action: action, resource: resource, condition: byOcel}] {
				t.Errorf("the deploy credential does not grant %s on %s limited to what Ocel tagged, so the function network cannot be changed or taken down, or reaches resources it does not own", action, resource)
			}
		}
	}

	reads := []string{
		"ec2:DescribeAddresses",
		"ec2:DescribeAddressesAttribute",
		"ec2:DescribeNatGateways",
		"ec2:DescribeNetworkInterfaces",
		"ec2:DescribePrefixLists",
		"ec2:DescribeRouteTables",
		"ec2:DescribeSecurityGroups",
		"ec2:DescribeSubnets",
		"ec2:DescribeVpcEndpoints",
	}
	for _, action := range reads {
		if !grants[grant{action: action, resource: iam.AnyResource, condition: unscoped}] {
			t.Errorf("the deploy credential does not grant %s, so the engine cannot read back what it built", action)
		}
	}
}
