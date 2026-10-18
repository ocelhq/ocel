package bootstrap

import (
	"maps"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/iam"
	"github.com/ocelhq/ocel/platform/aws/provider/tier/envsourcesync"
)

func TestTheVariablesKeyStackMakesNoEnvSourceSync(t *testing.T) {
	for _, tier := range bothTiers {
		for name, body := range map[string]string{
			"made":    partTemplate(provider.VariablesKeyPart, tier),
			"brought": renderedTemplate(variablesKeyPart, partInputs{ns: defaultNamespace, tier: tier, refs: fixtureRefs(), variablesKey: broughtKeyARN}).body,
		} {
			if strings.Contains(body, "EnvSourceSync") || strings.Contains(body, "AWS::Scheduler::") {
				t.Errorf("the %s %s variables-key stack still makes the env source sync, which the first scheduled env source adds as its own part", name, tier)
			}
		}
	}
}

func TestTheDeployCredentialMakesMovesForwardAndRemovesItsTiersEnvSourceSync(t *testing.T) {
	const (
		account = "111122223333"
		at      = "us-east-1:" + account
	)
	for _, tier := range bothTiers {
		_, deployDoc := renderedCredentialsOf(t, tier)
		names := envsourcesync.NamesFor(string(defaultNamespace), tier)
		function := "arn:aws:lambda:" + at + ":function:" + names.Function
		roleARN := func(name string) string {
			return "arn:aws:iam::" + account + ":role" + defaultNamespace.AppRolePathFor(tier) + name
		}
		group := "arn:aws:scheduler:" + at + ":schedule-group/" + names.ScheduleGroup
		schedule := "arn:aws:scheduler:" + at + ":schedule/" + names.ScheduleGroup + "/" + names.Schedule
		logs := "arn:aws:logs:" + at + ":log-group:" + names.LogGroup
		creating := requestContext{
			"aws:RequestTag/" + managedByTagKey:      managedByTagValue,
			"aws:RequestTag/" + naming.EnvTierTagKey: string(tier),
			"aws:PrincipalAccount":                   account,
			"iam:PermissionsBoundary":                "arn:aws:iam::" + account + ":policy/" + defaultNamespace.AppBoundaryNameFor(tier),
		}
		made := appResourceOf(tier, requestContext{
			"aws:PrincipalAccount":    account,
			"iam:PermissionsBoundary": creating["iam:PermissionsBoundary"],
		})
		with := func(base requestContext, also requestContext) requestContext {
			out := maps.Clone(base)
			maps.Copy(out, also)
			return out
		}
		for _, c := range []struct {
			action, arn string
			request     requestContext
		}{
			{"iam:CreateRole", roleARN(names.Role), creating},
			{"iam:CreateRole", roleARN(names.ScheduleRole), creating},
			{"iam:PutRolePolicy", roleARN(names.Role), made},
			{"iam:PassRole", roleARN(names.Role), requestContext{"iam:PassedToService": iam.LambdaPrincipal}},
			{"iam:PassRole", roleARN(names.ScheduleRole), requestContext{"iam:PassedToService": iam.SchedulerPrincipal}},
			{"iam:DeleteRolePolicy", roleARN(names.Role), made},
			{"iam:ListRolePolicies", roleARN(names.Role), made},
			{"iam:DeleteRole", roleARN(names.ScheduleRole), made},
			{"logs:CreateLogGroup", logs, creating},
			{"logs:PutRetentionPolicy", logs, made},
			{"logs:DeleteLogGroup", logs, made},
			{"lambda:CreateFunction", function, creating},
			{"lambda:UpdateFunctionCode", function, made},
			{"lambda:UpdateFunctionConfiguration", function, made},
			{"lambda:PutFunctionEventInvokeConfig", function + ":$LATEST", made},
			{"lambda:DeleteFunction", function, made},
			{"scheduler:CreateScheduleGroup", group, with(creating, nil)},
			{"scheduler:DeleteScheduleGroup", group, nil},
			{"scheduler:CreateSchedule", schedule, nil},
			{"scheduler:UpdateSchedule", schedule, nil},
			{"scheduler:DeleteSchedule", schedule, nil},
		} {
			if !allows(t, deployDoc, c.action, c.arn, c.request) {
				t.Errorf("the %s deploy credential cannot %s on %s, so a deploy cannot add, move forward or remove its env source sync", tier, c.action, c.arn)
			}
		}

		sibling := envsourcesync.NamesFor(string(defaultNamespace), tier.Sibling())
		if allows(t, deployDoc, "lambda:PutFunctionEventInvokeConfig", "arn:aws:lambda:"+at+":function:"+sibling.Function+":$LATEST", appResourceOf(tier.Sibling(), nil)) {
			t.Errorf("the %s deploy credential configures how the %s tier's env source sync is invoked", tier, tier.Sibling())
		}
		if allows(t, deployDoc, "lambda:PutFunctionEventInvokeConfig", "arn:aws:lambda:"+at+":function:ocel-app-shop-pr-7-web-a1b2c3:$LATEST", made) {
			t.Errorf("the %s deploy credential configures how an app function is invoked, beyond its env source sync", tier)
		}
	}
}

func TestTheBootstrapCredentialKeepsNoGrantOnlyAStackMadeEnvSourceSyncNeeded(t *testing.T) {
	for _, tier := range bothTiers {
		bootstrapDoc, _ := renderedCredentialsOf(t, tier)
		for g := range grantsOf(t, bootstrapDoc) {
			if strings.HasPrefix(g.action, "scheduler:") && strings.Contains(g.resource, defaultNamespace.CoreStackName()) {
				t.Errorf("the %s bootstrap credential grants %s on %s, a schedule group no bootstrap stack makes", tier, g.action, g.resource)
			}
			if strings.Contains(g.action, "EventInvokeConfig") && strings.Contains(g.resource, defaultNamespace.CoreStackName()) {
				t.Errorf("the %s bootstrap credential grants %s on %s, a function no bootstrap stack configures", tier, g.action, g.resource)
			}
		}
	}
}
