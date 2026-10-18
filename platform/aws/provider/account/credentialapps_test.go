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
	"github.com/ocelhq/ocel/platform/aws/provider/iam"
	"github.com/ocelhq/ocel/platform/aws/provider/tier/taginvalidator"
)

func TestTheDeployCredentialOwnsTheLogGroupsItCreates(t *testing.T) {
	grants := grantsOf(t, mustRender(t, DeployCredentialPermissions))
	want := map[string]string{
		"logs:CreateLogGroup":      conditionJSON(t, iam.TaggedOnCreate()),
		"logs:DeleteLogGroup":      conditionJSON(t, iam.TaggedByOcel()),
		"logs:FilterLogEvents":     conditionJSON(t, iam.TaggedByOcel()),
		"logs:ListTagsForResource": conditionJSON(t, iam.TaggedByOcel()),
		"logs:PutRetentionPolicy":  conditionJSON(t, iam.TaggedByOcel()),
		"logs:StartLiveTail":       conditionJSON(t, iam.TaggedByOcel()),
		"logs:TagResource":         conditionJSON(t, iam.TaggedByOcel()),
		"logs:UntagResource":       conditionJSON(t, iam.TaggedByOcel()),
	}
	for _, resource := range []string{iam.AppLogGroupARN, iam.FunctionLogGroupARN} {
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

func TestEveryCredentialScopesTaskDefinitionsToWhatAWSEvaluates(t *testing.T) {
	unscopable := []string{"ecs:DeregisterTaskDefinition", "ecs:DescribeTaskDefinition", "ecs:ListTaskDefinitions"}
	for purpose, document := range bothCredentials(t) {
		grants := grantsOf(t, document)
		if !grants[grant{action: "ecs:RegisterTaskDefinition", resource: iam.AppTaskDefinitionARN, condition: conditionJSON(t, iam.TaggedOnCreate())}] {
			t.Errorf("the %s credential does not grant ecs:RegisterTaskDefinition on %s under a create tag, so a container release either cannot register its family or registers one nothing marks as Ocel's", purpose, iam.AppTaskDefinitionARN)
		}
		for _, action := range unscopable {
			if !grants[grant{action: action, resource: iam.AnyResource, condition: conditionJSON(t, nil)}] {
				t.Errorf("the %s credential does not grant %s on %q, the only resource IAM evaluates it against, so a container release cannot read back or reclaim its task definition", purpose, action, iam.AnyResource)
			}
		}
		for g := range grants {
			if g.action == "ecs:RegisterTaskDefinition" && g.resource != iam.AppTaskDefinitionARN {
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
	for _, resource := range []string{iam.AppLogGroupARN, iam.FunctionLogGroupARN} {
		if got, deploy := logsGrantsOn(bootstrapGrants, resource), logsGrantsOn(deployGrants, resource); !maps.Equal(got, deploy) {
			t.Errorf("the bootstrap credential grants %v on %s, want the deploy credential's %v, so bootstrapping widens what a deploy may do to a log group", got, resource, deploy)
		}
	}
	for g := range bootstrapGrants {
		if !strings.HasPrefix(g.action, "logs:") {
			continue
		}
		switch g.resource {
		case iam.AppLogGroupARN, iam.FunctionLogGroupARN:
		case iam.AnyResource:
			if g.action != "logs:DescribeLogGroups" {
				t.Errorf("the bootstrap credential grants %s on %q, which reaches every log group in the account", g.action, g.resource)
			}
		default:
			t.Errorf("the bootstrap credential grants %s on %s, beyond the log groups a deploy owns: no bootstrap stack declares a function", g.action, g.resource)
		}
	}
}

func TestEveryCredentialTouchesOnlyEventSourceMappingsOfItsTagInvalidatorOrOcelTaggedOnesOfAppWorkers(t *testing.T) {
	ofAppWorkers := map[string]any{"lambda:FunctionArn": "arn:aws:lambda:*:*:function:ocel-app-*"}
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
		"sqs:CreateQueue":               {iam.AppQueueARN},
		"sqs:DeleteQueue":               {iam.AppQueueARN},
		"sns:CreateTopic":               {iam.AppTopicARN, iam.AppSubscriptionARN},
		"sns:DeleteTopic":               {iam.AppTopicARN, iam.AppSubscriptionARN},
		"scheduler:CreateSchedule":      {iam.AppScheduleARN},
		"scheduler:DeleteSchedule":      {iam.AppScheduleARN},
		"scheduler:CreateScheduleGroup": {iam.AppScheduleGroupARN},
		"scheduler:DeleteScheduleGroup": {iam.AppScheduleGroupARN},
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
	onCreate := conditionJSON(t, iam.ScalesECSServices(iam.TaggedOnCreate()))
	tagged := conditionJSON(t, iam.ScalesECSServices(iam.TaggedByOcel()))
	want := []grant{
		{action: "application-autoscaling:RegisterScalableTarget", resource: iam.ScalableTargetARN, condition: onCreate},
		{action: "application-autoscaling:RegisterScalableTarget", resource: iam.ScalableTargetARN, condition: tagged},
		{action: "application-autoscaling:DeregisterScalableTarget", resource: iam.ScalableTargetARN, condition: tagged},
		{action: "application-autoscaling:PutScalingPolicy", resource: iam.ScalableTargetARN, condition: tagged},
		{action: "application-autoscaling:DeleteScalingPolicy", resource: iam.ScalableTargetARN, condition: tagged},
		{action: "application-autoscaling:TagResource", resource: iam.ScalableTargetARN, condition: conditionJSON(t, iam.TaggedOnCreate())},
		{action: "application-autoscaling:TagResource", resource: iam.ScalableTargetARN, condition: conditionJSON(t, iam.TaggedByOcel())},
		{action: "application-autoscaling:UntagResource", resource: iam.ScalableTargetARN, condition: conditionJSON(t, iam.TaggedByOcel())},
		{action: "cloudwatch:PutMetricAlarm", resource: iam.ScalingAlarmARN, condition: "null"},
		{action: "cloudwatch:DeleteAlarms", resource: iam.ScalingAlarmARN, condition: "null"},
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
	requestProject   string
	resourceProject  string
}

func (r ecrRequest) context() map[string]string {
	context := map[string]string{
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

func allowsECR(t *testing.T, document string, r ecrRequest) bool {
	t.Helper()
	context := r.context()
	arn := "arn:aws:ecr:us-east-1:123456789012:repository/" + r.repository
	for _, statement := range parsePolicy(t, document).Statement {
		if !slices.Contains(stringsOf(t, statement.Action, "Action"), r.action) {
			continue
		}
		matches := slices.ContainsFunc(stringsOf(t, statement.Resource, "Resource"), func(resource string) bool {
			resolved, ok := resolveVariables(t, resource, context)
			return ok && conditionAdmits("StringLike", resolved, arn)
		})
		if matches && conditionHolds(t, statement.Condition, context) {
			return true
		}
	}
	return false
}

func TestEveryCredentialReachesImageRepositoriesOnlyOfTheProjectItsPrincipalIsTaggedWith(t *testing.T) {
	cases := []struct {
		name    string
		request ecrRequest
		allowed bool
	}{
		{"an untagged principal deletes in an Ocel repository tagged with a project", ecrRequest{action: "ecr:BatchDeleteImage", repository: "ocel/shop.web", resourceProject: "shop"}, true},
		{"an untagged principal removes an Ocel repository tagged with a project", ecrRequest{action: "ecr:DeleteRepository", repository: "ocel/shop.web", resourceProject: "shop"}, true},
		{"an untagged principal deletes in a repository no project is tagged on", ecrRequest{action: "ecr:BatchDeleteImage", repository: "ocel/shop.web"}, false},
		{"an untagged principal creates a repository tagged with its project", ecrRequest{action: "ecr:CreateRepository", repository: "ocel/shop.web", requestProject: "shop"}, true},
		{"an untagged principal creates a repository with no project tag", ecrRequest{action: "ecr:CreateRepository", repository: "ocel/shop.web"}, false},
		{"an untagged principal tags a repository made before the tag existed", ecrRequest{action: "ecr:TagResource", repository: "ocel/shop.web", requestProject: "shop"}, true},
		{"a tagged principal deletes in its project's repository", ecrRequest{action: "ecr:BatchDeleteImage", principalProject: "shop", repository: "ocel/shop.web", resourceProject: "shop"}, true},
		{"a tagged principal removes its project's repository", ecrRequest{action: "ecr:DeleteRepository", principalProject: "shop", repository: "ocel/shop.web", resourceProject: "shop"}, true},
		{"a tagged principal deletes in another project's repository", ecrRequest{action: "ecr:BatchDeleteImage", principalProject: "shop", repository: "ocel/blog.web", resourceProject: "blog"}, false},
		{"a tagged principal deletes in another project's repository it tagged as its own", ecrRequest{action: "ecr:BatchDeleteImage", principalProject: "shop", repository: "ocel/blog.web", resourceProject: "shop"}, false},
		{"a tagged principal deletes in its project's repository tagged with another project", ecrRequest{action: "ecr:BatchDeleteImage", principalProject: "shop", repository: "ocel/shop.web", resourceProject: "blog"}, false},
		{"a tagged principal creates its project's repository", ecrRequest{action: "ecr:CreateRepository", principalProject: "shop", repository: "ocel/shop.web", requestProject: "shop"}, true},
		{"a tagged principal creates another project's repository tagged as its own", ecrRequest{action: "ecr:CreateRepository", principalProject: "shop", repository: "ocel/blog.web", requestProject: "shop"}, false},
		{"a tagged principal creates its project's repository tagged with another project", ecrRequest{action: "ecr:CreateRepository", principalProject: "shop", repository: "ocel/shop.web", requestProject: "blog"}, false},
		{"a tagged principal creates a repository whose project only starts with its own", ecrRequest{action: "ecr:CreateRepository", principalProject: "shop", repository: "ocel/shop-two.web", requestProject: "shop"}, false},
		{"a tagged principal tags its project's repository", ecrRequest{action: "ecr:TagResource", principalProject: "shop", repository: "ocel/shop.web", requestProject: "shop"}, true},
		{"a tagged principal tags another project's repository as its own", ecrRequest{action: "ecr:TagResource", principalProject: "shop", repository: "ocel/blog.web", requestProject: "shop", resourceProject: "blog"}, false},
		{"a tagged principal pushes to its project's repository", ecrRequest{action: "ecr:PutImage", principalProject: "shop", repository: "ocel/shop.web", resourceProject: "shop"}, true},
		{"any principal deletes outside the ocel namespace", ecrRequest{action: "ecr:BatchDeleteImage", repository: "theirs/web", resourceProject: "shop"}, false},
	}
	for purpose, document := range bothCredentials(t) {
		for _, c := range cases {
			if got := allowsECR(t, document, c.request); got != c.allowed {
				t.Errorf("the %s credential: %s is allowed = %t, want %t", purpose, c.name, got, c.allowed)
			}
		}
	}
}

func TestEveryCredentialRunsAndReachesOnlyTheTasksOfABastionCluster(t *testing.T) {
	onBastion := conditionJSON(t, map[string]any{"ArnEquals": map[string]any{"ecs:cluster": bastion.ClusterARN}})
	want := []grant{
		{action: "ecs:RunTask", resource: bastion.TaskDefinitionARN, condition: onBastion},
		{action: "ecs:DescribeTasks", resource: bastion.TaskARN, condition: "null"},
		{action: "ecs:StopTask", resource: bastion.TaskARN, condition: "null"},
		{action: "ecs:ExecuteCommand", resource: bastion.ClusterARN, condition: "null"},
		{action: "ecs:ExecuteCommand", resource: bastion.TaskARN, condition: "null"},
		{action: "ecs:ListTasks", resource: iam.AnyResource, condition: onBastion},
		{action: "ecs:DescribeClusters", resource: bastion.ClusterARN, condition: "null"},
		{action: "ecs:DeleteTaskDefinitions", resource: bastion.TaskDefinitionARN, condition: "null"},
		{action: "ssm:StartSession", resource: bastion.TaskARN, condition: "null"},
		{action: "ssm:StartSession", resource: bastion.PortForwardingDocumentARN, condition: "null"},
	}
	for purpose, document := range bothCredentials(t) {
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
	scopes := []string{bastion.ClusterARN, bastion.TaskARN, bastion.TaskDefinitionARN, bastion.PortForwardingDocumentARN}
	for purpose, document := range bothCredentials(t) {
		for g := range grantsOf(t, document) {
			if slices.Contains(reachable, g.action) && !slices.Contains(scopes, g.resource) {
				t.Errorf("the %s credential grants %s on %s, which reaches past the bastion cluster's tasks and the one port forwarding document", purpose, g.action, g.resource)
			}
		}
	}
}

func TestEveryCredentialListsTasksOnlyOfABastionClusterAndAgainstTheResourceAWSEvaluates(t *testing.T) {
	onBastion := conditionJSON(t, map[string]any{"ArnEquals": map[string]any{"ecs:cluster": bastion.ClusterARN}})
	for purpose, document := range bothCredentials(t) {
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
	request := taggedWithTier(tier, requestContext{"aws:ResourceTag/" + iam.ManagedByTagKey: iam.ManagedByTagValue})
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
					"aws:RequestTag/" + iam.ManagedByTagKey:  iam.ManagedByTagValue,
					"aws:RequestTag/" + naming.EnvTierTagKey: string(of),
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
		return "arn:aws:iam::111122223333:role/" + bastion.NameFor(tier)
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

func TestTheDeployCredentialRaisesAndRemovesBacklogAlarmsOnlyUnderTheAppScope(t *testing.T) {
	grants := grantsOf(t, mustRender(t, DeployCredentialPermissions))
	for _, action := range []string{
		"cloudwatch:PutMetricAlarm",
		"cloudwatch:DeleteAlarms",
		"cloudwatch:TagResource",
		"cloudwatch:UntagResource",
		"cloudwatch:ListTagsForResource",
	} {
		if wanted := (grant{action: action, resource: iam.AppAlarmARN, condition: "null"}); !grants[wanted] {
			t.Errorf("the deploy credential does not grant %s on %s: a container worker at no tasks is started by an alarm on its queues", action, iam.AppAlarmARN)
		}
	}
	for g := range grants {
		if g.action == "cloudwatch:PutMetricAlarm" && g.resource != iam.AppAlarmARN && g.resource != iam.ScalingAlarmARN {
			t.Errorf("the deploy credential grants cloudwatch:PutMetricAlarm on %s, want only the alarms Ocel names and the ones target tracking makes", g.resource)
		}
	}
}

func TestTheDeployCredentialBuildsAndTearsDownTheFunctionNetworkOnlyWhereOcelTaggedIt(t *testing.T) {
	grants := grantsOf(t, mustRender(t, DeployCredentialPermissions))
	onCreate := conditionJSON(t, iam.TaggedOnCreate())
	byOcel := conditionJSON(t, iam.TaggedByOcel())
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
