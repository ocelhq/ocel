package bootstrap

import (
	"context"
	"fmt"
	"testing"

	"gopkg.in/yaml.v3"
)

const (
	lambdaDescriptionLimit   = 256
	iamDescriptionLimit      = 1000
	kmsDescriptionLimit      = 8192
	ssmDescriptionLimit      = 1024
	templateDescriptionLimit = 1024
	fieldDescriptionLimit    = 1024

	houseDescriptionLimit = 256
)

type describedTemplate struct {
	Description string `yaml:"Description"`
	Parameters  map[string]struct {
		Description string `yaml:"Description"`
	} `yaml:"Parameters"`
	Resources map[string]struct {
		Type     string `yaml:"Type"`
		Metadata struct {
			Description string `yaml:"Description"`
		} `yaml:"Metadata"`
		Properties struct {
			Description string `yaml:"Description"`
		} `yaml:"Properties"`
	} `yaml:"Resources"`
	Outputs map[string]struct {
		Description string `yaml:"Description"`
	} `yaml:"Outputs"`
}

func propertyDescriptionLimit(resourceType string) int {
	switch resourceType {
	case "AWS::Lambda::Function":
		return lambdaDescriptionLimit
	case "AWS::IAM::Role", "AWS::IAM::ManagedPolicy":
		return iamDescriptionLimit
	case "AWS::KMS::Key":
		return kmsDescriptionLimit
	case "AWS::SSM::Parameter":
		return ssmDescriptionLimit
	}
	return houseDescriptionLimit
}

func everyRenderedTemplate() map[string]string {
	rendered := map[string]string{
		"core/" + ClassProduction: stackTemplate(),
		"core/" + ClassPreview:    previewStackTemplate(),
	}
	for _, name := range featureNames() {
		for _, class := range []string{ClassProduction, ClassPreview} {
			rendered[name+"/"+class] = featureTemplateWith(name, class, everyFeature())
		}
	}
	return rendered
}

func TestRenderedDescriptionsFitTheirLimits(t *testing.T) {
	for name, body := range everyRenderedTemplate() {
		t.Run(name, func(t *testing.T) {
			var tmpl describedTemplate
			if err := yaml.Unmarshal([]byte(body), &tmpl); err != nil {
				t.Fatalf("template is not valid YAML: %v", err)
			}

			within := func(where, description string, limit int) {
				t.Helper()
				if len(description) > limit {
					t.Errorf("%s description is %d characters, over the %d AWS accepts: %q", where, len(description), limit, description)
				}
			}

			within("the template's own", tmpl.Description, templateDescriptionLimit)
			for param, declared := range tmpl.Parameters {
				within("parameter "+param, declared.Description, fieldDescriptionLimit)
			}
			for output, declared := range tmpl.Outputs {
				within("output "+output, declared.Description, fieldDescriptionLimit)
			}
			for resource, declared := range tmpl.Resources {
				within(
					fmt.Sprintf("%s (%s)", resource, declared.Type),
					declared.Properties.Description,
					propertyDescriptionLimit(declared.Type),
				)
				within(resource+" metadata", declared.Metadata.Description, houseDescriptionLimit)
			}
		})
	}
}

func TestSSMDescriptionsFitTheirLimits(t *testing.T) {
	for _, class := range []string{ClassProduction, ClassPreview} {
		t.Run(class, func(t *testing.T) {
			cfn, ssmc, iamc := newFakeCFN(), newFakeSSM(), &fakeIAM{}
			frontedBy(t, &fakeEdge{kind: "cloudflare"})

			if err := Run(context.Background(), apisOf(cfn, ssmc, iamc, preloadedStore()), class, everything(), nil, nil); err != nil {
				t.Fatalf("Run: %v", err)
			}
			if len(ssmc.descriptions) == 0 {
				t.Fatal("the bootstrap wrote no SSM parameter, so nothing was checked")
			}
			for name, description := range ssmc.descriptions {
				if len(description) > ssmDescriptionLimit {
					t.Errorf("%s description is %d characters, over the %d SSM accepts: %q", name, len(description), ssmDescriptionLimit, description)
				}
			}
		})
	}
}
