package bootstrap

import (
	"encoding/json"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/ocelhq/ocel/pkg/environment"
)

type declaredTemplate struct {
	Parameters map[string]struct {
		Type        string `json:"Type"`
		Description string `json:"Description"`
	} `json:"Parameters"`
	Resources map[string]json.RawMessage `json:"Resources"`
}

var substitutedName = regexp.MustCompile(`\$\{([A-Za-z0-9:.]+)\}`)

var pseudoParameters = []string{"AWS::AccountId", "AWS::Region", "AWS::Partition", "AWS::URLSuffix", "AWS::StackName", "AWS::NoValue"}

func referencedNames(value any) []string {
	var out []string
	switch v := value.(type) {
	case string:
		for _, m := range substitutedName.FindAllStringSubmatch(v, -1) {
			out = append(out, strings.Split(m[1], ".")[0])
		}
	case []any:
		for _, item := range v {
			out = append(out, referencedNames(item)...)
		}
	case map[string]any:
		for key, item := range v {
			switch key {
			case "Ref":
				if name, ok := item.(string); ok {
					out = append(out, name)
				}
			case "Fn::GetAtt":
				if attribute, ok := item.([]any); ok && len(attribute) > 0 {
					if name, ok := attribute[0].(string); ok {
						out = append(out, name)
					}
				}
			}
			out = append(out, referencedNames(item)...)
		}
	}
	return out
}

func TestEveryPartTemplateTakesItsUpstreamFromParametersAndResolvesEveryName(t *testing.T) {
	for _, name := range partNames() {
		for _, tier := range []environment.Tier{environment.TierProduction, environment.TierPreview} {
			t.Run(name+"/"+string(tier), func(t *testing.T) {
				stack := partStackFor(name, tier, everyPart())

				var tmpl declaredTemplate
				if err := json.Unmarshal([]byte(stack.body), &tmpl); err != nil {
					t.Fatalf("template is not valid JSON: %v", err)
				}

				t.Run("every declared parameter is handed a value", func(t *testing.T) {
					supplied := map[string]string{}
					for _, p := range stack.params {
						supplied[aws.ToString(p.ParameterKey)] = aws.ToString(p.ParameterValue)
					}
					for key := range tmpl.Parameters {
						value, ok := supplied[key]
						if !ok {
							t.Errorf("parameter %s is declared and never handed a value; CloudFormation would refuse the stack", key)
							continue
						}
						if value == "" {
							t.Errorf("parameter %s is handed an empty value; whatever upstream stack should have produced it did not", key)
						}
					}
					for key := range supplied {
						if _, ok := tmpl.Parameters[key]; !ok {
							t.Errorf("value handed for %s, which the template never declares", key)
						}
					}
				})

				t.Run("reaches across stacks by parameter alone", func(t *testing.T) {
					for _, forbidden := range []string{"ImportValue", "Fn::ImportValue", `"Export"`} {
						if strings.Contains(stack.body, forbidden) {
							t.Errorf("template uses %s; a part reads its upstream from a parameter, never from a CloudFormation export", forbidden)
						}
					}
				})

				t.Run("every name it references it can resolve", func(t *testing.T) {
					known := slices.Clone(pseudoParameters)
					for key := range tmpl.Parameters {
						known = append(known, key)
					}
					for key := range tmpl.Resources {
						known = append(known, key)
					}
					var document any
					if err := json.Unmarshal([]byte(stack.body), &document); err != nil {
						t.Fatal(err)
					}
					for _, ref := range referencedNames(document) {
						if !slices.Contains(known, ref) {
							t.Errorf("template references %q, which is neither a parameter it declares nor a resource it defines", ref)
						}
					}
				})

				t.Run("every node function runs nodejs24.x, the runtime app functions run", func(t *testing.T) {
					for key, raw := range tmpl.Resources {
						var declared struct {
							Type       string `json:"Type"`
							Properties struct {
								Runtime string `json:"Runtime"`
							} `json:"Properties"`
						}
						if err := json.Unmarshal(raw, &declared); err != nil {
							t.Fatalf("resource %s does not decode: %v", key, err)
						}
						if declared.Type != "AWS::Lambda::Function" || !strings.HasPrefix(declared.Properties.Runtime, "nodejs") {
							continue
						}
						if declared.Properties.Runtime != "nodejs24.x" {
							t.Errorf("%s runs %s, want nodejs24.x: the runtime app functions run and CI tests on", key, declared.Properties.Runtime)
						}
					}
				})
			})
		}
	}
}
