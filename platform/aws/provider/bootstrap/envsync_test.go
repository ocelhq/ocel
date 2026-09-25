package bootstrap

import (
	"maps"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/ocelhq/ocel/platform/aws/provider/payloads"
)

func fixtureEnvSyncCode() payloads.Placement {
	return payloads.Placement{Bucket: fixtureBucket, Key: payloads.Key(envSyncKeyPrefix, payloads.EnvSync().SHA256)}
}

type envSyncStatement struct {
	Effect    string                    `yaml:"Effect"`
	Principal map[string]any            `yaml:"Principal"`
	Action    any                       `yaml:"Action"`
	Resource  any                       `yaml:"Resource"`
	Condition map[string]map[string]any `yaml:"Condition"`
}

type envSyncTemplate struct {
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
				Statement []envSyncStatement `yaml:"Statement"`
			} `yaml:"AssumeRolePolicyDocument"`
			ManagedPolicyArns []string `yaml:"ManagedPolicyArns"`
			Policies          []struct {
				PolicyDocument struct {
					Statement []envSyncStatement `yaml:"Statement"`
				} `yaml:"PolicyDocument"`
			} `yaml:"Policies"`
			Name               string `yaml:"Name"`
			ScheduleExpression string `yaml:"ScheduleExpression"`
			State              string `yaml:"State"`
			FlexibleTimeWindow struct {
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

func parseEnvSyncTemplate(t *testing.T, body string) envSyncTemplate {
	t.Helper()
	var tmpl envSyncTemplate
	if err := yaml.Unmarshal([]byte(body), &tmpl); err != nil {
		t.Fatalf("template is not valid YAML: %v", err)
	}
	return tmpl
}

func listed(value any) []string {
	switch v := value.(type) {
	case string:
		return []string{v}
	case []any:
		out := make([]string, 0, len(v))
		for _, one := range v {
			out = append(out, one.(string))
		}
		return out
	}
	return nil
}

type envSyncCase struct {
	name  string
	class string
	body  string
	key   string
}

func envSyncCases() []envSyncCase {
	var cases []envSyncCase
	for _, class := range []string{ClassProduction, ClassPreview} {
		cases = append(cases,
			envSyncCase{"made/" + class, class, featureTemplate(FeatureVarsKey, class), "VarsKey.Arn"},
			envSyncCase{"brought/" + class, class, varsKeyFeature.template(featureInputs{
				ns: defaultNamespace, class: class, code: fixturePayloads(), refs: fixtureRefs(), varsKey: broughtKeyARN,
			}).body, broughtKeyARN},
		)
	}
	return cases
}

func TestEveryVarsKeyStackStandsAnEnvSyncer(t *testing.T) {
	for _, tc := range envSyncCases() {
		t.Run(tc.name, func(t *testing.T) {
			tmpl := parseEnvSyncTemplate(t, tc.body)

			fn, ok := tmpl.Resources["EnvSync"]
			if !ok || fn.Type != "AWS::Lambda::Function" {
				t.Fatalf("the vars-key stack declares no EnvSync function, so a standing env source is only ever read at deploy")
			}
			if fn.Properties.Code.S3Bucket != fixtureEnvSyncCode().Bucket || fn.Properties.Code.S3Key != fixtureEnvSyncCode().Key {
				t.Errorf("EnvSync Code = %+v, want the placed envsync payload", fn.Properties.Code)
			}
			if fn.Properties.Runtime != "provided.al2023" || fn.Properties.Handler != "bootstrap" || !slices.Equal(fn.Properties.Architectures, []string{"arm64"}) {
				t.Errorf("EnvSync runs %s/%s on %v, want the arm64 Go bootstrap on provided.al2023", fn.Properties.Runtime, fn.Properties.Handler, fn.Properties.Architectures)
			}
			if fn.Properties.Role != "EnvSyncRole.Arn" {
				t.Errorf("EnvSync Role = %q, want its own EnvSyncRole", fn.Properties.Role)
			}
			want := map[string]string{
				"OCEL_VARS_TABLE":  paramVarsTableName,
				"OCEL_VARS_KEY":    tc.key,
				"OCEL_INFRA_CLASS": tc.class,
			}
			if got := fn.Properties.Environment.Variables; !maps.Equal(got, want) {
				t.Errorf("EnvSync environment = %v, want exactly %v: where the class's values live, never a value itself", got, want)
			}
		})
	}
}

func TestTheEnvSyncerRunsEveryMinuteOnItsOwnInvokeRole(t *testing.T) {
	for _, tc := range envSyncCases() {
		t.Run(tc.name, func(t *testing.T) {
			tmpl := parseEnvSyncTemplate(t, tc.body)

			schedule, ok := tmpl.Resources["EnvSyncSchedule"]
			if !ok || schedule.Type != "AWS::Scheduler::Schedule" {
				t.Fatal("the vars-key stack declares no EnvSyncSchedule, so the syncer never runs")
			}
			p := schedule.Properties
			if p.ScheduleExpression != "rate(1 minute)" {
				t.Errorf("ScheduleExpression = %q, want rate(1 minute)", p.ScheduleExpression)
			}
			if p.FlexibleTimeWindow.Mode != "OFF" {
				t.Errorf("FlexibleTimeWindow.Mode = %q, want OFF: a window only delays a poll that is due", p.FlexibleTimeWindow.Mode)
			}
			if p.State != "ENABLED" {
				t.Errorf("State = %q, want ENABLED", p.State)
			}
			if p.Target.Arn != "EnvSync.Arn" || p.Target.RoleArn != "EnvSyncScheduleRole.Arn" {
				t.Errorf("Target = %+v, want EnvSync invoked through EnvSyncScheduleRole", p.Target)
			}
			if r := p.Target.RetryPolicy.MaximumRetryAttempts; r == nil || *r != 0 {
				t.Errorf("MaximumRetryAttempts = %v, want 0: the next minute's invocation is the retry", r)
			}
			if want := defaultNamespace.envSyncScheduleName(tc.class); p.Name != want {
				t.Errorf("Name = %q, want %q, the name the bootstrap credential is scoped to", p.Name, want)
			}

			role := tmpl.Resources["EnvSyncScheduleRole"]
			if role.Type != "AWS::IAM::Role" {
				t.Fatal("the vars-key stack declares no EnvSyncScheduleRole for the schedule to invoke through")
			}
			trust := role.Properties.AssumeRolePolicyDocument.Statement
			if len(trust) != 1 || trust[0].Principal["Service"] != "scheduler.amazonaws.com" {
				t.Fatalf("EnvSyncScheduleRole trusts %+v, want EventBridge Scheduler alone", trust)
			}
			if trust[0].Condition["StringEquals"]["aws:SourceAccount"] != "${AWS::AccountId}" {
				t.Errorf("EnvSyncScheduleRole trust carries %v, want it bound to this account's schedules", trust[0].Condition)
			}
			if len(role.Properties.ManagedPolicyArns) != 0 {
				t.Errorf("EnvSyncScheduleRole attaches %v, want only its inline invoke grant", role.Properties.ManagedPolicyArns)
			}
			var grants []string
			for _, policy := range role.Properties.Policies {
				for _, st := range policy.PolicyDocument.Statement {
					for _, action := range listed(st.Action) {
						for _, resource := range listed(st.Resource) {
							grants = append(grants, action+" on "+resource)
						}
					}
				}
			}
			if want := []string{"lambda:InvokeFunction on EnvSync.Arn"}; !slices.Equal(grants, want) {
				t.Errorf("EnvSyncScheduleRole grants %v, want exactly %v", grants, want)
			}
		})
	}
}

func TestTheEnvSyncerReachesOnlyTheVarsTableTheKeyAndItsLogs(t *testing.T) {
	for _, tc := range envSyncCases() {
		t.Run(tc.name, func(t *testing.T) {
			role := parseEnvSyncTemplate(t, tc.body).Resources["EnvSyncRole"]
			if role.Type != "AWS::IAM::Role" {
				t.Fatal("the vars-key stack declares no EnvSyncRole")
			}
			trust := role.Properties.AssumeRolePolicyDocument.Statement
			if len(trust) != 1 || trust[0].Principal["Service"] != LambdaServicePrincipal {
				t.Errorf("EnvSyncRole trusts %+v, want Lambda alone", trust)
			}
			if len(role.Properties.ManagedPolicyArns) != 0 {
				t.Errorf("EnvSyncRole attaches %v, whose log grant reaches every group in the account", role.Properties.ManagedPolicyArns)
			}
			granted := map[string][]string{}
			for _, policy := range role.Properties.Policies {
				for _, st := range policy.PolicyDocument.Statement {
					if st.Effect != "Allow" {
						t.Errorf("EnvSyncRole carries a %s statement", st.Effect)
					}
					for _, resource := range listed(st.Resource) {
						granted[resource] = append(granted[resource], listed(st.Action)...)
					}
				}
			}
			for resource := range granted {
				slices.Sort(granted[resource])
			}
			want := map[string][]string{
				paramVarsTableARN:     {"dynamodb:DeleteItem", "dynamodb:GetItem", "dynamodb:PutItem", "dynamodb:Query"},
				tc.key:                {"kms:Decrypt", "kms:Encrypt"},
				"EnvSyncLogGroup.Arn": {"logs:CreateLogStream", "logs:PutLogEvents"},
			}
			if !maps.EqualFunc(granted, want, slices.Equal[[]string]) {
				t.Errorf("EnvSyncRole grants %v, want exactly %v", granted, want)
			}
		})
	}
}

func TestTheEnvSyncScheduleIsNamedWhereTheBootstrapCredentialReaches(t *testing.T) {
	bootstrapGrants := grantsOf(t, mustRender(t, BootstrapCredentialPermissions))
	for _, class := range []string{ClassProduction, ClassPreview} {
		arn := "arn:aws:scheduler:us-east-1:111122223333:schedule/default/" + defaultNamespace.envSyncScheduleName(class)
		for _, action := range []string{"scheduler:CreateSchedule", "scheduler:DeleteSchedule", "scheduler:GetSchedule", "scheduler:UpdateSchedule"} {
			reached := false
			for g := range bootstrapGrants {
				if g.action == action && wildcardMatch(g.resource, arn) {
					reached = true
				}
			}
			if !reached {
				t.Errorf("the bootstrap tier grants no %s on %s, so CloudFormation cannot stand or reclaim the %s syncer's schedule", action, arn, class)
			}
		}
	}
	for g := range bootstrapGrants {
		if strings.HasPrefix(g.action, "scheduler:") && !strings.HasPrefix(g.resource, "arn:aws:scheduler:*:*:schedule/default/"+defaultNamespace.CoreStackName()) {
			t.Errorf("the bootstrap tier grants %s on %s, beyond the schedules a bootstrap names", g.action, g.resource)
		}
	}
	passed := false
	for g := range bootstrapGrants {
		if g.action == "iam:PassRole" && strings.Contains(g.condition, `"iam:PassedToService":"scheduler.amazonaws.com"`) {
			passed = g.resource == defaultNamespace.ScopedARNs().bootstrapRole
			if !passed {
				t.Errorf("the bootstrap tier passes %s to Scheduler, want only the roles a bootstrap stack makes", g.resource)
			}
		}
	}
	if !passed {
		t.Error("the bootstrap tier passes no role to Scheduler, so the schedule cannot name the role it invokes through")
	}
	for g := range grantsOf(t, mustRender(t, DeployCredentialPermissions)) {
		if strings.HasPrefix(g.action, "scheduler:") {
			t.Errorf("the deploy tier grants %s on %s; a deploy never stands a schedule", g.action, g.resource)
		}
	}
}

func wildcardMatch(pattern, s string) bool {
	head, tail, wild := strings.Cut(pattern, "*")
	if !wild {
		return pattern == s
	}
	if !strings.HasPrefix(s, head) {
		return false
	}
	rest := s[len(head):]
	for i := 0; i <= len(rest); i++ {
		if wildcardMatch(tail, rest[i:]) {
			return true
		}
	}
	return false
}
