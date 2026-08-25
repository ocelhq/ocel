package bootstrap

import (
	"context"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type residentEdge struct {
	*fakeEdge
	class string
}

func fronting(kind edge.Kind) *residentEdge {
	return &residentEdge{fakeEdge: &fakeEdge{kind: kind}}
}

func (r *residentEdge) CoreStack(class string) CoreFragment {
	r.class = class
	return CoreFragment{
		Resources: `  EdgeRoutes:
    Type: Test::Edge::Routes
    Properties:
      Name: "ocel-routes-` + class + `"
`,
		Outputs: `  EdgeRoutesArn:
    Description: "the store this edge routes with"
    Value: !Ref EdgeRoutes
`,
	}
}

func TestTheCoreStackCarriesTheSelectedEdgesResources(t *testing.T) {
	t.Run("the selected edge writes its own into the template", func(t *testing.T) {
		cfn, ssmc, iamc := newFakeCFN(), newFakeSSM(), &fakeIAM{}
		resident := fronting("resident")

		if err := run(context.Background(), apisFronting(cfn, ssmc, iamc, preloadedStore(), resident), productionBootstrap(), Request{}, nil, nil); err != nil {
			t.Fatalf("run: %v", err)
		}

		body := cfn.templates[StackName]
		if !strings.Contains(body, "Type: Test::Edge::Routes") {
			t.Errorf("the core template carries no resource of the edge it was bootstrapped against:\n%s", body)
		}
		if !strings.Contains(body, "EdgeRoutesArn:") {
			t.Error("the core template publishes no output for the edge's resources, so a deploy cannot reach them")
		}
		if resident.class != ClassProduction {
			t.Errorf("the edge was asked for its %q resources, want %q", resident.class, ClassProduction)
		}
	})

	t.Run("an edge that keeps nothing in the core stack leaves it alone", func(t *testing.T) {
		cfn, ssmc, iamc := newFakeCFN(), newFakeSSM(), &fakeIAM{}

		if err := run(context.Background(), apisFronting(cfn, ssmc, iamc, preloadedStore(), &fakeEdge{kind: "elsewhere"}), productionBootstrap(), Request{}, nil, nil); err != nil {
			t.Fatalf("run: %v", err)
		}

		if body := cfn.templates[StackName]; strings.Contains(body, "EdgeRoutes") {
			t.Errorf("the core template carries an edge's resources though the edge selected keeps none there:\n%s", body)
		}
	})
}

func TestThePlanNamesTheSelectedEdgesResourcesAmongTheCoreStacksOwn(t *testing.T) {
	ctx := context.Background()
	cfn := newFakeCFN()
	described := providerkit.Bootstrap{Class: providerkit.Class(ClassProduction)}
	groups, err := PlanChanges(ctx, cfn, ClassProduction, fronting("resident"), Request{},
		providerkit.DeriveGroups(NameStacks(described), Catalogue(),
			providerkit.BootstrapRequest{Class: providerkit.Class(ClassProduction)}))
	if err != nil {
		t.Fatalf("PlanChanges: %v", err)
	}

	core := groupNamed(t, groups, StackName)
	routes := changeNamed(t, core, "EdgeRoutes")
	if routes.Kind != "Test::Edge::Routes" {
		t.Errorf("the plan names EdgeRoutes a %q, want the type the edge declared", routes.Kind)
	}
	if routes.Action != providerkit.ActionCreate {
		t.Errorf("EdgeRoutes is %q on an account with no bootstrap, want it created", routes.Action)
	}
}

func TestIndentLeavesBlankLinesBlank(t *testing.T) {
	got := Indent("first\n\n  second", 4)
	if want := "    first\n\n      second\n"; got != want {
		t.Errorf("Indent = %q, want %q", got, want)
	}
}
