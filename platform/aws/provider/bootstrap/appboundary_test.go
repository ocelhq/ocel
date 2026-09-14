package bootstrap

import (
	"slices"
	"strings"
	"testing"

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
			PolicyDocument    struct {
				Statement []boundaryStatement `yaml:"Statement"`
			} `yaml:"PolicyDocument"`
		} `yaml:"Properties"`
	} `yaml:"Resources"`
}

func boundaryStatements(t *testing.T, class string) []boundaryStatement {
	t.Helper()
	var tmpl boundaryTemplate
	if err := yaml.Unmarshal([]byte(coreStackTemplate(defaultNamespace, class)), &tmpl); err != nil {
		t.Fatalf("template is not valid YAML: %v", err)
	}
	boundary, ok := tmpl.Resources["AppBoundary"]
	if !ok {
		t.Fatal("the core stack carries no AppBoundary, so nothing caps the roles a deploy mints")
	}
	if boundary.Type != "AWS::IAM::ManagedPolicy" {
		t.Errorf("AppBoundary Type = %q, want AWS::IAM::ManagedPolicy", boundary.Type)
	}
	if got, want := boundary.Properties.ManagedPolicyName, defaultNamespace.AppBoundaryNameFor(class); got != want {
		t.Errorf("ManagedPolicyName = %q, want %q", got, want)
	}
	return boundary.Properties.PolicyDocument.Statement
}

func TestTheAppBoundaryFencesKeysSecretsAndParametersToWhatAnAppOfItsClassOwns(t *testing.T) {
	for _, class := range []string{ClassProduction, ClassPreview} {
		t.Run(class, func(t *testing.T) {
			for _, st := range boundaryStatements(t, class) {
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
						if got, want := aliases["kms:ResourceAliases"], defaultNamespace.varsKeyAliasFor(class); got != want {
							t.Errorf("%s is admitted under %v, want it pinned to %s alone: a %s role must not open what the other class sealed", action, st.Condition, want, class)
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

func TestTheAppBoundaryStillAdmitsWhatADeployGrantsARole(t *testing.T) {
	for _, class := range []string{ClassProduction, ClassPreview} {
		t.Run(class, func(t *testing.T) {
			var admitted []string
			for _, st := range boundaryStatements(t, class) {
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
			} {
				if !slices.Contains(admitted, action) {
					t.Errorf("the boundary no longer admits %s, which a deploy writes onto an app role", action)
				}
			}
		})
	}
}
