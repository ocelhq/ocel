package bootstrap

import (
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/payloads"
)

func fixtureEnvSourceSyncCode() payloads.Placement {
	return payloads.Placement{Bucket: fixtureBucket, Key: payloads.Key(envSourceSyncKeyPrefix, payloads.EnvSourceSync().SHA256)}
}

type envSourceSyncStatement struct {
	Effect    string                    `yaml:"Effect"`
	Principal map[string]any            `yaml:"Principal"`
	Action    any                       `yaml:"Action"`
	Resource  any                       `yaml:"Resource"`
	Condition map[string]map[string]any `yaml:"Condition"`
}

type envSourceSyncTemplate struct {
	Resources map[string]struct {
		Type       string `yaml:"Type"`
		Properties struct {
			Runtime       string   `yaml:"Runtime"`
			Handler       string   `yaml:"Handler"`
			Architectures []string `yaml:"Architectures"`
			Role          string   `yaml:"Role"`
			Code          struct {
				S3Bucket string `yaml:"S3Bucket"`
				S3Key    string `yaml:"S3Key"`
			} `yaml:"Code"`
			Environment struct {
				Variables map[string]string `yaml:"Variables"`
			} `yaml:"Environment"`
			AssumeRolePolicyDocument struct {
				Statement []envSourceSyncStatement `yaml:"Statement"`
			} `yaml:"AssumeRolePolicyDocument"`
			ManagedPolicyArns []string `yaml:"ManagedPolicyArns"`
			Policies          []struct {
				PolicyDocument struct {
					Statement []envSourceSyncStatement `yaml:"Statement"`
				} `yaml:"PolicyDocument"`
			} `yaml:"Policies"`
			FunctionName             string `yaml:"FunctionName"`
			Qualifier                string `yaml:"Qualifier"`
			MaximumRetryAttempts     *int   `yaml:"MaximumRetryAttempts"`
			MaximumEventAgeInSeconds *int   `yaml:"MaximumEventAgeInSeconds"`
			Name                     string `yaml:"Name"`
			GroupName                string `yaml:"GroupName"`
			ScheduleExpression       string `yaml:"ScheduleExpression"`
			State                    string `yaml:"State"`
			FlexibleTimeWindow       struct {
				Mode string `yaml:"Mode"`
			} `yaml:"FlexibleTimeWindow"`
			Target struct {
				Arn         string `yaml:"Arn"`
				RoleArn     string `yaml:"RoleArn"`
				RetryPolicy struct {
					MaximumRetryAttempts *int `yaml:"MaximumRetryAttempts"`
				} `yaml:"RetryPolicy"`
			} `yaml:"Target"`
		} `yaml:"Properties"`
	} `yaml:"Resources"`
}

func parseEnvSourceSyncTemplate(t *testing.T, body string) envSourceSyncTemplate {
	t.Helper()
	var tmpl envSourceSyncTemplate
	if err := yaml.Unmarshal([]byte(body), &tmpl); err != nil {
		t.Fatalf("template is not valid YAML: %v", err)
	}
	return tmpl
}

func valuesOf(value any) []string {
	switch v := value.(type) {
	case string:
		return []string{v}
	case []any:
		out := make([]string, 0, len(v))
		for _, one := range v {
			out = append(out, fmt.Sprint(one))
		}
		return out
	}
	return nil
}

type variablesKeyStackCase struct {
	name string
	tier environment.Tier
	body string
	key  string
}

func variablesKeyStackCases() []variablesKeyStackCase {
	var cases []variablesKeyStackCase
	for _, tier := range []environment.Tier{environment.TierProduction, environment.TierPreview} {
		cases = append(cases,
			variablesKeyStackCase{"made/" + string(tier), tier, featureTemplate(provider.FeatureVariablesKey, tier), "VariablesKey.Arn"},
			variablesKeyStackCase{"brought/" + string(tier), tier, variablesKeyFeature.template(featureInputs{
				ns: defaultNamespace, tier: tier, code: fixturePayloads(), refs: fixtureRefs(), variablesKey: broughtKeyARN,
			}).body, broughtKeyARN},
		)
	}
	return cases
}

func TestEveryVariablesKeyStackMakesAnEnvSourceSync(t *testing.T) {
	for _, tc := range variablesKeyStackCases() {
		t.Run(tc.name, func(t *testing.T) {
			fn, ok := parseEnvSourceSyncTemplate(t, tc.body).Resources["EnvSourceSync"]
			if !ok || fn.Type != "AWS::Lambda::Function" {
				t.Fatal("the variables-key stack declares no EnvSourceSync function, so a scheduled env source is only ever read at deploy")
			}
			if code := fixtureEnvSourceSyncCode(); fn.Properties.Code.S3Bucket != code.Bucket || fn.Properties.Code.S3Key != code.Key {
				t.Errorf("EnvSourceSync Code = %+v, want the placed envsourcesync payload", fn.Properties.Code)
			}
			if fn.Properties.Runtime != "provided.al2023" || fn.Properties.Handler != "bootstrap" || !slices.Equal(fn.Properties.Architectures, []string{"arm64"}) {
				t.Errorf("EnvSourceSync runs %s/%s on %v, want the arm64 Go bootstrap on provided.al2023", fn.Properties.Runtime, fn.Properties.Handler, fn.Properties.Architectures)
			}
			if fn.Properties.Role != "EnvSourceSyncRole.Arn" {
				t.Errorf("EnvSourceSync Role = %q, want its own EnvSourceSyncRole", fn.Properties.Role)
			}
			want := map[string]string{
				"OCEL_VARIABLES_TABLE": paramVariablesTableName,
				"OCEL_VARIABLES_KEY":   tc.key,
				"OCEL_INFRA_TIER":      string(tc.tier),
			}
			if got := fn.Properties.Environment.Variables; !maps.Equal(got, want) {
				t.Errorf("EnvSourceSync environment = %v, want exactly %v: where the tier's values are, never a value itself", got, want)
			}
		})
	}
}

func TestTheEnvSourceSyncRunsEveryMinuteThroughItsOwnInvokeRole(t *testing.T) {
	for _, tc := range variablesKeyStackCases() {
		t.Run(tc.name, func(t *testing.T) {
			tmpl := parseEnvSourceSyncTemplate(t, tc.body)

			group, ok := tmpl.Resources["EnvSourceSyncScheduleGroup"]
			if !ok || group.Type != "AWS::Scheduler::ScheduleGroup" {
				t.Fatal("the variables-key stack declares no EnvSourceSyncScheduleGroup, so its schedule has no group the bootstrap credential is scoped to")
			}
			if want := defaultNamespace.envSourceSyncScheduleGroupName(tc.tier); group.Properties.Name != want {
				t.Errorf("EnvSourceSyncScheduleGroup Name = %q, want %q, the name the bootstrap credential is scoped to", group.Properties.Name, want)
			}

			schedule, ok := tmpl.Resources["EnvSourceSyncSchedule"]
			if !ok || schedule.Type != "AWS::Scheduler::Schedule" {
				t.Fatal("the variables-key stack declares no EnvSourceSyncSchedule, so the sync never runs")
			}
			p := schedule.Properties
			if want := defaultNamespace.envSourceSyncScheduleName(tc.tier); p.Name != want || p.GroupName != "EnvSourceSyncScheduleGroup" {
				t.Errorf("schedule is %q in group %q, want %q in EnvSourceSyncScheduleGroup", p.Name, p.GroupName, want)
			}
			if p.ScheduleExpression != "rate(1 minute)" {
				t.Errorf("ScheduleExpression = %q, want rate(1 minute)", p.ScheduleExpression)
			}
			if p.FlexibleTimeWindow.Mode != "OFF" {
				t.Errorf("FlexibleTimeWindow.Mode = %q, want OFF: a window only delays a sync that is due", p.FlexibleTimeWindow.Mode)
			}
			if p.State != "ENABLED" {
				t.Errorf("State = %q, want ENABLED", p.State)
			}
			if p.Target.Arn != "EnvSourceSync.Arn" || p.Target.RoleArn != "EnvSourceSyncScheduleRole.Arn" {
				t.Errorf("Target = %+v, want EnvSourceSync invoked through EnvSourceSyncScheduleRole", p.Target)
			}
			if r := p.Target.RetryPolicy.MaximumRetryAttempts; r == nil || *r != 0 {
				t.Errorf("MaximumRetryAttempts = %v, want 0: the next minute's invocation is the retry", r)
			}

			role := tmpl.Resources["EnvSourceSyncScheduleRole"]
			if role.Type != "AWS::IAM::Role" {
				t.Fatal("the variables-key stack declares no EnvSourceSyncScheduleRole for the schedule to invoke through")
			}
			trust := role.Properties.AssumeRolePolicyDocument.Statement
			if len(trust) != 1 || trust[0].Principal["Service"] != "scheduler.amazonaws.com" {
				t.Fatalf("EnvSourceSyncScheduleRole trusts %+v, want EventBridge Scheduler alone", trust)
			}
			if trust[0].Condition["StringEquals"]["aws:SourceAccount"] != "${AWS::AccountId}" {
				t.Errorf("EnvSourceSyncScheduleRole trust has %v, want it bound to this account", trust[0].Condition)
			}
			if trust[0].Condition["ArnEquals"]["aws:SourceArn"] != "EnvSourceSyncScheduleGroup.Arn" {
				t.Errorf("EnvSourceSyncScheduleRole trust has %v, want it bound to this tier's schedule group", trust[0].Condition)
			}
			if len(role.Properties.ManagedPolicyArns) != 0 {
				t.Errorf("EnvSourceSyncScheduleRole attaches %v, want only its inline invoke grant", role.Properties.ManagedPolicyArns)
			}
			var grants []string
			for _, policy := range role.Properties.Policies {
				for _, st := range policy.PolicyDocument.Statement {
					for _, action := range valuesOf(st.Action) {
						for _, resource := range valuesOf(st.Resource) {
							grants = append(grants, action+" on "+resource)
						}
					}
				}
			}
			if want := []string{"lambda:InvokeFunction on EnvSourceSync.Arn"}; !slices.Equal(grants, want) {
				t.Errorf("EnvSourceSyncScheduleRole grants %v, want exactly %v", grants, want)
			}
		})
	}
}

func TestNoTwoVariablesKeyStacksInOneAccountNameTheSameSchedule(t *testing.T) {
	seen := map[string]string{}
	for _, ns := range []Namespace{defaultNamespace, Namespace("j-1-deploy-next"), Namespace("j-2-deploy-next")} {
		for _, tier := range []environment.Tier{environment.TierProduction, environment.TierPreview} {
			body := variablesKeyFeature.template(featureInputs{
				ns: ns, tier: tier, code: fixturePayloads(), refs: fixtureRefs(), variablesKey: broughtKeyARN,
			}).body
			name := parseEnvSourceSyncTemplate(t, body).Resources["EnvSourceSyncSchedule"].Properties.Name
			stack := ns.featureStackName(provider.FeatureVariablesKey, tier)
			if other, taken := seen[name]; taken {
				t.Errorf("%s and %s both name their schedule %q, and CloudFormation identifies a schedule by its name alone, whatever its group, so the second stack fails to create", other, stack, name)
			}
			seen[name] = stack
		}
	}
}

func TestAnEnvSourceSyncThatFailsOrIsThrottledIsNeverRunLateOverTheNextMinutes(t *testing.T) {
	for _, tc := range variablesKeyStackCases() {
		t.Run(tc.name, func(t *testing.T) {
			config, ok := parseEnvSourceSyncTemplate(t, tc.body).Resources["EnvSourceSyncInvokeConfig"]
			if !ok || config.Type != "AWS::Lambda::EventInvokeConfig" {
				t.Fatal("the variables-key stack declares no EnvSourceSyncInvokeConfig, so Lambda retries a failed sync twice and a throttled one for six hours, over the syncs after it")
			}
			p := config.Properties
			if p.FunctionName != "EnvSourceSync" || p.Qualifier != "$LATEST" {
				t.Errorf("EnvSourceSyncInvokeConfig configures %s:%s, want EnvSourceSync:$LATEST, the version the schedule invokes", p.FunctionName, p.Qualifier)
			}
			if r := p.MaximumRetryAttempts; r == nil || *r != 0 {
				t.Errorf("MaximumRetryAttempts = %v, want 0: the next minute's invocation is the retry", r)
			}
			if age := p.MaximumEventAgeInSeconds; age == nil || *age != 60 {
				t.Errorf("MaximumEventAgeInSeconds = %v, want 60: an invocation a minute old is the next minute's, already on its way", age)
			}
		})
	}
}

func TestTheBootstrapCredentialConfiguresHowTheEnvSourceSyncIsInvokedAndNoOtherFunction(t *testing.T) {
	bootstrapGrants := grantsOf(t, mustRender(t, BootstrapCredentialPermissions))
	actions := []string{
		"lambda:PutFunctionEventInvokeConfig", "lambda:GetFunctionEventInvokeConfig",
		"lambda:UpdateFunctionEventInvokeConfig", "lambda:DeleteFunctionEventInvokeConfig",
	}
	for _, tier := range []environment.Tier{environment.TierProduction, environment.TierPreview} {
		function := "arn:aws:lambda:us-east-1:111122223333:function:" + defaultNamespace.featureStackName(provider.FeatureVariablesKey, tier) + "-EnvSourceSync-A1B2C3"
		for _, action := range actions {
			reached := false
			for g := range bootstrapGrants {
				reached = reached || (g.action == action && matchesIAMPattern(g.resource, function))
			}
			if !reached {
				t.Errorf("the bootstrap credential grants no %s on %s, so CloudFormation cannot configure how the %s sync is invoked", action, function, tier)
			}
		}
	}
	for g := range bootstrapGrants {
		if slices.Contains(actions, g.action) && g.resource != defaultNamespace.ScopedARNs(environment.TierProduction).bootstrapFunction {
			t.Errorf("the bootstrap credential grants %s on %s, beyond the functions a bootstrap names", g.action, g.resource)
		}
	}
	for g := range grantsOf(t, mustRender(t, DeployCredentialPermissions)) {
		if slices.Contains(actions, g.action) {
			t.Errorf("the deploy credential grants %s on %s; a deploy never configures the env source sync", g.action, g.resource)
		}
	}
}

func TestTheEnvSourceSyncReachesOnlyTheVariablesTableTheKeyAndItsLogs(t *testing.T) {
	for _, tc := range variablesKeyStackCases() {
		t.Run(tc.name, func(t *testing.T) {
			role := parseEnvSourceSyncTemplate(t, tc.body).Resources["EnvSourceSyncRole"]
			if role.Type != "AWS::IAM::Role" {
				t.Fatal("the variables-key stack declares no EnvSourceSyncRole")
			}
			trust := role.Properties.AssumeRolePolicyDocument.Statement
			if len(trust) != 1 || trust[0].Principal["Service"] != LambdaServicePrincipal {
				t.Errorf("EnvSourceSyncRole trusts %+v, want Lambda alone", trust)
			}
			if len(role.Properties.ManagedPolicyArns) != 0 {
				t.Errorf("EnvSourceSyncRole attaches %v, whose log grant reaches every group in the account", role.Properties.ManagedPolicyArns)
			}
			granted := map[string][]string{}
			for _, policy := range role.Properties.Policies {
				for _, st := range policy.PolicyDocument.Statement {
					if st.Effect != "Allow" {
						t.Errorf("EnvSourceSyncRole has a %s statement", st.Effect)
					}
					for _, resource := range valuesOf(st.Resource) {
						granted[resource] = append(granted[resource], valuesOf(st.Action)...)
					}
				}
			}
			for resource := range granted {
				slices.Sort(granted[resource])
			}
			want := map[string][]string{
				paramVariablesTableARN:      {"dynamodb:DeleteItem", "dynamodb:GetItem", "dynamodb:PutItem", "dynamodb:Query"},
				tc.key:                      {"kms:Decrypt", "kms:Encrypt"},
				"EnvSourceSyncLogGroup.Arn": {"logs:CreateLogStream", "logs:PutLogEvents"},
			}
			if !maps.EqualFunc(granted, want, slices.Equal[[]string]) {
				t.Errorf("EnvSourceSyncRole grants %v, want exactly %v", granted, want)
			}
		})
	}
}

func TestTheBootstrapCredentialReachesTheEnvSourceSyncScheduleThroughItsGroupAndNothingElse(t *testing.T) {
	bootstrapGrants := grantsOf(t, mustRender(t, BootstrapCredentialPermissions))
	for _, tier := range []environment.Tier{environment.TierProduction, environment.TierPreview} {
		bootstrapDoc, _ := renderedCredentialsOf(t, tier)
		ofTier := grantsOf(t, bootstrapDoc)
		reaches := func(action, arn string) bool {
			for g := range ofTier {
				if g.action == action && matchesIAMPattern(g.resource, arn) {
					return true
				}
			}
			return false
		}
		group := defaultNamespace.envSourceSyncScheduleGroupName(tier)
		groupARN := "arn:aws:scheduler:us-east-1:111122223333:schedule-group/" + group
		for _, action := range []string{
			"scheduler:CreateScheduleGroup", "scheduler:DeleteScheduleGroup", "scheduler:GetScheduleGroup",
			"scheduler:ListTagsForResource", "scheduler:TagResource", "scheduler:UntagResource",
		} {
			if !reaches(action, groupARN) {
				t.Errorf("the bootstrap credential grants no %s on %s, so CloudFormation cannot make or remove the %s sync's schedule group", action, groupARN, tier)
			}
		}
		scheduleARN := "arn:aws:scheduler:us-east-1:111122223333:schedule/" + group + "/" + defaultNamespace.envSourceSyncScheduleName(tier)
		for _, action := range []string{"scheduler:CreateSchedule", "scheduler:DeleteSchedule", "scheduler:GetSchedule", "scheduler:UpdateSchedule"} {
			if !reaches(action, scheduleARN) {
				t.Errorf("the bootstrap credential grants no %s on %s, so CloudFormation cannot make or remove the %s sync's schedule", action, scheduleARN, tier)
			}
		}
	}
	core := defaultNamespace.CoreStackName()
	for g := range bootstrapGrants {
		if !strings.HasPrefix(g.action, "scheduler:") {
			continue
		}
		if g.resource == appScheduleGroupARN || g.resource == appScheduleARN {
			continue
		}
		if !strings.HasPrefix(g.resource, "arn:aws:scheduler:*:*:schedule-group/"+core) && !strings.HasPrefix(g.resource, "arn:aws:scheduler:*:*:schedule/"+core) {
			t.Errorf("the bootstrap credential grants %s on %s, beyond the schedule groups a bootstrap or an app names", g.action, g.resource)
		}
	}
	passed := false
	for g := range bootstrapGrants {
		if g.action == "iam:PassRole" && strings.Contains(g.condition, `"iam:PassedToService":"scheduler.amazonaws.com"`) {
			if g.resource == appRoleARN && strings.Contains(g.condition, `"aws:ResourceTag/ocel:managed-by":"ocel"`) {
				continue
			}
			passed = slices.Contains(defaultNamespace.ScopedARNs(environment.TierProduction).bootstrapRoles, g.resource)
			if !passed {
				t.Errorf("the bootstrap credential passes %s to Scheduler, want only the roles a bootstrap stack makes or an app role Ocel tagged", g.resource)
			}
		}
	}
	if !passed {
		t.Error("the bootstrap credential passes no role to Scheduler, so the schedule cannot name the role it invokes through")
	}
	for g := range grantsOf(t, mustRender(t, DeployCredentialPermissions)) {
		if strings.HasPrefix(g.action, "scheduler:") && g.resource != appScheduleGroupARN && g.resource != appScheduleARN {
			t.Errorf("the deploy credential grants %s on %s; a deploy makes schedules only in an app's own group", g.action, g.resource)
		}
	}
}

func matchesIAMPattern(pattern, s string) bool {
	head, tail, wild := strings.Cut(pattern, "*")
	if !wild {
		return pattern == s
	}
	if !strings.HasPrefix(s, head) {
		return false
	}
	rest := s[len(head):]
	for i := 0; i <= len(rest); i++ {
		if matchesIAMPattern(tail, rest[i:]) {
			return true
		}
	}
	return false
}
