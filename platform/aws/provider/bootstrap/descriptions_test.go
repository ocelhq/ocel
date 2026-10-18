package bootstrap

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
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
	Description string `json:"Description"`
	Parameters  map[string]struct {
		Description string `json:"Description"`
	} `json:"Parameters"`
	Resources map[string]struct {
		Type     string `json:"Type"`
		Metadata struct {
			Description string `json:"Description"`
		} `json:"Metadata"`
		Properties struct {
			Description string `json:"Description"`
		} `json:"Properties"`
	} `json:"Resources"`
	Outputs map[string]struct {
		Description string `json:"Description"`
	} `json:"Outputs"`
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
		"core/" + string(environment.TierProduction): coreBody(defaultNamespace, environment.TierProduction, ""),
		"core/" + string(environment.TierPreview):    coreBody(defaultNamespace, environment.TierPreview, ""),
	}
	for _, name := range partNames() {
		for _, tier := range []environment.Tier{environment.TierProduction, environment.TierPreview} {
			rendered[name+"/"+string(tier)] = partTemplateWith(name, tier, everyPart())
		}
	}
	return rendered
}

func TestRenderedDescriptionsFitTheirLimits(t *testing.T) {
	for name, body := range everyRenderedTemplate() {
		t.Run(name, func(t *testing.T) {
			var tmpl describedTemplate
			if err := json.Unmarshal([]byte(body), &tmpl); err != nil {
				t.Fatalf("template is not valid JSON: %v", err)
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
	for _, tier := range []environment.Tier{environment.TierProduction, environment.TierPreview} {
		t.Run(string(tier), func(t *testing.T) {
			stacks, ssmc, iamc := newFakeCFN(), newFakeSSM(), &fakeIAM{}
			frontedBy(t, &fakeEdge{kind: "cloudflare"})

			if err := Run(context.Background(), apisOf(stacks, ssmc, iamc, preloadedStore()), defaultNamespace, tier, everything(), nil); err != nil {
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
