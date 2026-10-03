package bootstrap

import (
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"gopkg.in/yaml.v3"
)

type boundaryStatement struct {
	Effect    string         `yaml:"Effect"`
	Action    any            `yaml:"Action"`
	Resource  string         `yaml:"Resource"`
	Condition map[string]any `yaml:"Condition"`
}

type boundaryTemplate struct {
	Resources map[string]struct {
		Type       string `yaml:"Type"`
		Properties struct {
			ManagedPolicyName string `yaml:"ManagedPolicyName"`
			Description       string `yaml:"Description"`
			PolicyDocument    struct {
				Statement []boundaryStatement `yaml:"Statement"`
			} `yaml:"PolicyDocument"`
		} `yaml:"Properties"`
	} `yaml:"Resources"`
}

func boundaryStatements(t *testing.T, tier environment.Tier, broughtKey string) []boundaryStatement {
	t.Helper()
	var tmpl boundaryTemplate
	if err := yaml.Unmarshal([]byte(coreStackTemplate(defaultNamespace, tier, broughtKey)), &tmpl); err != nil {
		t.Fatalf("template is not valid YAML: %v", err)
	}
	boundary, ok := tmpl.Resources["AppBoundary"]
	if !ok {
		t.Fatal("the core stack has no AppBoundary, so nothing caps the roles a deploy mints")
	}
	if boundary.Type != "AWS::IAM::ManagedPolicy" {
		t.Errorf("AppBoundary Type = %q, want AWS::IAM::ManagedPolicy", boundary.Type)
	}
	if got, want := boundary.Properties.ManagedPolicyName, defaultNamespace.AppBoundaryNameFor(tier); got != want {
		t.Errorf("ManagedPolicyName = %q, want %q", got, want)
	}
	return boundary.Properties.PolicyDocument.Statement
}

func TestTheAppBoundaryKeepsTheDescriptionItWasCreatedWith(t *testing.T) {
	var tmpl boundaryTemplate
	if err := yaml.Unmarshal([]byte(coreStackTemplate(defaultNamespace, environment.TierPreview, "")), &tmpl); err != nil {
		t.Fatalf("template is not valid YAML: %v", err)
	}
	want := "Permissions boundary for the roles Ocel creates for apps in the preview class."
	if got := tmpl.Resources["AppBoundary"].Properties.Description; got != want {
		t.Errorf("AppBoundary Description = %q, want %q: CloudFormation replaces a managed policy whose description changes, and the replacement cannot take the ManagedPolicyName the policy already holds", got, want)
	}
}

func TestTheAppBoundaryFencesKeysSecretsAndParametersToWhatAnAppOfItsTierOwns(t *testing.T) {
	for _, tier := range []environment.Tier{environment.TierProduction, environment.TierPreview} {
		t.Run(string(tier), func(t *testing.T) {
			for _, st := range boundaryStatements(t, tier, "") {
				if st.Effect != "Allow" {
					t.Errorf("statement Effect = %q, want Allow", st.Effect)
				}
				for _, action := range yamlStrings(st.Action) {
					service, _, _ := strings.Cut(action, ":")
					switch service {
					case "ssm":
						t.Errorf("the boundary admits %s; the only parameters under Ocel's name are the passphrase, the edge credentials and the origin secret, and no app may read them", action)
					case "kms":
						aliases, _ := st.Condition["ForAnyValue:StringEquals"].(map[string]any)
						if got, want := aliases["kms:ResourceAliases"], defaultNamespace.variablesKeyAliasFor(tier); got != want {
							t.Errorf("%s is admitted under %v, want it pinned to %s alone: a %s role must not open what the other tier sealed", action, st.Condition, want, tier)
						}
						if st.Resource != "*" {
							t.Errorf("%s is admitted on %q; the alias Ocel put on the key it made is the fence, and no key ARN is known before that stack is provisioned", action, st.Resource)
						}
					case "secretsmanager":
						if st.Resource != appSecretARN {
							t.Errorf("%s is admitted on %q, want the RDS-managed master secrets alone", action, st.Resource)
						}
						like, _ := st.Condition["StringLike"].(map[string]any)
						if got := like["aws:ResourceTag/"+managedSecretClusterTagKey]; got != appClusterARN {
							t.Errorf("%s is admitted under %v, want it pinned to a cluster in the app scope through the tag RDS stamps on the secret", action, st.Condition)
						}
					}
				}
			}
		})
	}
}

func TestTheAppBoundaryAdmitsABroughtKeyByItsARNAndPutsNoAliasOnIt(t *testing.T) {
	for _, tier := range []environment.Tier{environment.TierProduction, environment.TierPreview} {
		t.Run(string(tier), func(t *testing.T) {
			var keyed []boundaryStatement
			for _, st := range boundaryStatements(t, tier, broughtKeyARN) {
				if slices.ContainsFunc(yamlStrings(st.Action), func(action string) bool { return strings.HasPrefix(action, "kms:") }) {
					keyed = append(keyed, st)
				}
			}
			if len(keyed) != 1 {
				t.Fatalf("the boundary admits kms in %d statements, want exactly one naming the brought key", len(keyed))
			}
			if keyed[0].Resource != broughtKeyARN {
				t.Errorf("kms is admitted on %q, want the key this account brought and nothing wider: Ocel can put no alias on a key whose policy it does not write", keyed[0].Resource)
			}
			if _, aliased := keyed[0].Condition["ForAnyValue:StringEquals"]; aliased || len(keyed[0].Condition) != 0 {
				t.Errorf("the brought key is admitted under %v, want it fenced by ARN alone", keyed[0].Condition)
			}
		})
	}
}

func TestTheAppBoundaryStillAdmitsWhatADeployGrantsARole(t *testing.T) {
	for _, tier := range []environment.Tier{environment.TierProduction, environment.TierPreview} {
		t.Run(string(tier), func(t *testing.T) {
			var admitted []string
			for _, st := range boundaryStatements(t, tier, "") {
				admitted = append(admitted, yamlStrings(st.Action)...)
			}
			for _, action := range []string{
				"kms:Decrypt",
				"dynamodb:Query",
				"s3:GetObject",
				"s3:PutObject",
				"lambda:InvokeFunctionUrl",
				"secretsmanager:GetSecretValue",
				"ecr:BatchGetImage",
				"appsync:EventConnect",
				"appsync:EventPublish",
				"appsync:GetApi",
			} {
				if !slices.Contains(admitted, action) {
					t.Errorf("the boundary no longer admits %s, which a deploy writes onto an app role", action)
				}
			}
		})
	}
}
