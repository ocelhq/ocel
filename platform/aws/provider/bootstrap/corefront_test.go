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
	class  string
	output string
}

func fronting(kind edge.Kind) *residentEdge {
	return &residentEdge{fakeEdge: &fakeEdge{kind: kind}, output: "EdgeRoutesArn"}
}

func (r *residentEdge) CoreStack(class string) CoreFragment {
	r.class = class
	return CoreFragment{
		Resources: `  EdgeRoutes:
    Type: Test::Edge::Routes
    Properties:
      Name: "ocel-routes-` + class + `"
`,
		Outputs: `  ` + r.output + `:
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
	front := fronting("resident")
	read, err := Read(ctx, cfn, ClassProduction, front)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	described := providerkit.Bootstrap{Class: providerkit.Class(ClassProduction)}
	groups, err := PlanChanges(ctx, cfn, read, front, Request{},
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

func TestAnAccountStandingAgainstAnotherEdgeIsRefusedBeforeItIsRewritten(t *testing.T) {
	ctx := context.Background()

	standing := func(t *testing.T, front edge.Edge) (*fakeCFN, *fakeSSM, *fakeIAM) {
		t.Helper()
		cfn, ssmc, iamc := newFakeCFN(), newFakeSSM(), &fakeIAM{}
		if err := run(ctx, apisFronting(cfn, ssmc, iamc, preloadedStore(), front), productionBootstrap(), Request{}, nil, nil); err != nil {
			t.Fatalf("run: %v", err)
		}
		return cfn, ssmc, iamc
	}

	newcomer := func() *residentEdge {
		other := fronting("newcomer")
		other.output = "EdgeNewcomerArn"
		return other
	}

	t.Run("the run refuses before the core stack is rewritten", func(t *testing.T) {
		cfn, ssmc, iamc := standing(t, fronting("resident"))
		before, writes := cfn.templates[StackName], len(cfn.applied)

		err := run(ctx, apisFronting(cfn, ssmc, iamc, preloadedStore(), newcomer()), productionBootstrap(), Request{}, nil, nil)
		if err == nil {
			t.Fatal("a bootstrap against a second edge succeeded, taking the standing edge's resources with it")
		}
		if !strings.Contains(err.Error(), "ocel bootstrap destroy production") {
			t.Errorf("error = %v, want it to name the destroy an edge move goes through", err)
		}
		if cfn.templates[StackName] != before || len(cfn.applied) != writes {
			t.Errorf("the core stack was rewritten by a run that refused, so the standing edge's resources are already gone")
		}
	})

	t.Run("the plan refuses rather than showing the switch as changes", func(t *testing.T) {
		cfn, _, _ := standing(t, fronting("resident"))
		other := newcomer()

		read, err := Read(ctx, cfn, ClassProduction, other)
		if err != nil {
			t.Fatalf("Read: %v", err)
		}
		if _, err := PlanChanges(ctx, cfn, read, other, Request{}, nil); err == nil {
			t.Fatal("the plan reported changes for a switch the apply would refuse")
		}
	})

	t.Run("an edge that keeps nothing in the core stack is a switch too", func(t *testing.T) {
		cfn, ssmc, iamc := standing(t, &fakeEdge{kind: "elsewhere"})

		if err := run(ctx, apisFronting(cfn, ssmc, iamc, preloadedStore(), fronting("resident")), productionBootstrap(), Request{}, nil, nil); err == nil {
			t.Fatal("an account fronted from off-cloud was rebootstrapped against a resident edge")
		}
	})

	t.Run("the edge it already stands against is left to run", func(t *testing.T) {
		cfn, ssmc, iamc := standing(t, fronting("resident"))

		if err := run(ctx, apisFronting(cfn, ssmc, iamc, preloadedStore(), fronting("resident")), productionBootstrap(), Request{}, nil, nil); err != nil {
			t.Fatalf("re-running against the standing edge: %v", err)
		}
	})
}

func TestIndentLeavesBlankLinesBlank(t *testing.T) {
	got := Indent("first\n\n  second", 4)
	if want := "    first\n\n      second\n"; got != want {
		t.Errorf("Indent = %q, want %q", got, want)
	}
}
