package bootstrap

import (
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"gopkg.in/yaml.v3"
)

type declaredTemplate struct {
	Parameters map[string]struct {
		Type        string `yaml:"Type"`
		Description string `yaml:"Description"`
	} `yaml:"Parameters"`
	Resources map[string]yaml.Node `yaml:"Resources"`
	Outputs   map[string]struct {
		Value string `yaml:"Value"`
	} `yaml:"Outputs"`
}

var (
	refRE    = regexp.MustCompile(`!Ref\s+([A-Za-z0-9:]+)`)
	getAttRE = regexp.MustCompile(`!GetAtt\s+([A-Za-z0-9]+)\.`)
	subRE    = regexp.MustCompile(`\$\{([A-Za-z0-9:.]+)\}`)
)

var pseudoParameters = []string{"AWS::AccountId", "AWS::Region", "AWS::Partition", "AWS::URLSuffix", "AWS::StackName", "AWS::NoValue"}

func TestFeatureTemplates(t *testing.T) {
	for _, name := range featureNames() {
		for _, class := range []string{ClassProduction, ClassPreview} {
			t.Run(name+"/"+class, func(t *testing.T) {
				stack := featureStackFor(name, class, everyFeature())

				var tmpl declaredTemplate
				if err := yaml.Unmarshal([]byte(stack.body), &tmpl); err != nil {
					t.Fatalf("template is not valid YAML: %v", err)
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
					for _, forbidden := range []string{"ImportValue", "Fn::ImportValue", "Export:"} {
						if strings.Contains(stack.body, forbidden) {
							t.Errorf("template uses %s; a feature reads its upstream from a parameter, never from a CloudFormation export", forbidden)
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
					var referenced []string
					for _, m := range refRE.FindAllStringSubmatch(stack.body, -1) {
						referenced = append(referenced, m[1])
					}
					for _, m := range getAttRE.FindAllStringSubmatch(stack.body, -1) {
						referenced = append(referenced, m[1])
					}
					for _, m := range subRE.FindAllStringSubmatch(stack.body, -1) {
						referenced = append(referenced, strings.Split(m[1], ".")[0])
					}
					for _, ref := range referenced {
						if !slices.Contains(known, ref) {
							t.Errorf("template references %q, which is neither a parameter it declares nor a resource it holds", ref)
						}
					}
				})

				t.Run("no version output", func(t *testing.T) {
					if _, ok := tmpl.Outputs["BootstrapVersion"]; ok {
						t.Error("the substrate's shape is carried by the ocel:schema tag; no stack Output restates it")
					}
				})
			})
		}
	}
}
