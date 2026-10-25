package variables_test

import (
	"context"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/variables"
	"github.com/ocelhq/ocel/pkg/buildoutput"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
)

func boundScope(framework string) variables.Scope {
	return variables.Scope{
		Apps: []variables.App{{Name: "web", Folder: "/web", Framework: framework}},
		Tier: environmentv1.Tier_TIER_PRODUCTION,
		Bound: []project.BoundVariable{
			{Key: "ORIGIN_CERT", Binding: "origin", Property: "certificate"},
			{Key: "ORIGIN_KEY", Binding: "origin", Property: "key"},
		},
	}
}

func declareOrigin(t *testing.T, g *variables.Declarations) {
	t.Helper()
	declare(t, g,
		def("ORIGIN_CERT", resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN),
		def("ORIGIN_KEY", resourcesv1.VariableClass_VARIABLE_CLASS_SECRET),
	)
}

func TestABoundVariableIsHandedToTheDeployAsTheRecordAndPropertyItReads(t *testing.T) {
	t.Parallel()

	values := newFakeValues()
	g := prefetched(t, values, boundScope(buildoutput.FrameworkNode))
	declareOrigin(t, g)

	if err := g.RefuseIncomplete(); err != nil {
		t.Fatalf("RefuseIncomplete = %v, want a bound variable never missing a value", err)
	}
	if err := g.RefuseBoundVariableConflicts(); err != nil {
		t.Fatalf("RefuseBoundVariableConflicts = %v", err)
	}
	resolved, err := g.ResolveVariables(context.Background(), []variables.App{{Name: "web", Folder: "/web"}})
	if err != nil {
		t.Fatalf("ResolveVariables: %v", err)
	}
	want := map[string]variables.Variable{
		"ORIGIN_CERT": {Key: "ORIGIN_CERT", Class: resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN, Binding: "origin", Property: "certificate"},
		"ORIGIN_KEY":  {Key: "ORIGIN_KEY", Class: resourcesv1.VariableClass_VARIABLE_CLASS_SECRET, Binding: "origin", Property: "key"},
	}
	if len(resolved["web"]) != len(want) {
		t.Fatalf("web resolves %+v, want %+v", resolved["web"], want)
	}
	for _, got := range resolved["web"] {
		if got != want[got.Key] {
			t.Errorf("web resolves %+v, want %+v", got, want[got.Key])
		}
	}
	secrets, err := g.RevealSecrets(context.Background(), []string{"web"})
	if err != nil {
		t.Fatalf("RevealSecrets: %v", err)
	}
	if len(secrets["web"]) != 0 || len(values.revealed) != 0 {
		t.Errorf("revealed %v from cells %v, want no store read for a variable a record serves", secrets, values.revealed)
	}
}

func TestABoundVariableWithAValueSetForItIsRefused(t *testing.T) {
	t.Parallel()

	for name, set := range map[string]func(*fakeValues){
		"at the project root": func(v *fakeValues) { v.set("ORIGIN_CERT", "", "by hand") },
		"in a folder":         func(v *fakeValues) { v.set("ORIGIN_CERT", "/web", "by hand") },
		"for one environment": func(v *fakeValues) { v.override("ORIGIN_CERT", "", "pr-1", "by hand") },
	} {
		values := newFakeValues()
		set(values)
		g := prefetched(t, values, boundScope(buildoutput.FrameworkNode))
		declareOrigin(t, g)

		err := g.RefuseBoundVariableConflicts()
		for _, want := range []string{"ORIGIN_CERT", "@origin.certificate", "ocel env rm ORIGIN_CERT"} {
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Errorf("%s: RefuseBoundVariableConflicts = %v, want it refused naming %q", name, err, want)
			}
		}
	}
}

func TestABoundVariableNoAppDeclaresIsRefused(t *testing.T) {
	t.Parallel()

	g := prefetched(t, newFakeValues(), boundScope(buildoutput.FrameworkNode))
	declare(t, g, def("ORIGIN_CERT", resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN))

	err := g.RefuseBoundVariableConflicts()
	if err == nil || !strings.Contains(err.Error(), "ORIGIN_KEY") || !strings.Contains(err.Error(), "no app declares") {
		t.Errorf("RefuseBoundVariableConflicts = %v, want ORIGIN_KEY refused as bound and never declared", err)
	}
}

func TestABoundVariableABuildInlinesIsRefused(t *testing.T) {
	t.Parallel()

	scope := boundScope(buildoutput.FrameworkNext)
	scope.Bound = []project.BoundVariable{{Key: "NEXT_PUBLIC_ORIGIN", Binding: "origin", Property: "certificate"}}
	g := prefetched(t, newFakeValues(), scope)
	declare(t, g, def("NEXT_PUBLIC_ORIGIN", resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN))

	err := g.RefuseBoundVariableConflicts()
	for _, want := range []string{"NEXT_PUBLIC_ORIGIN", "web", "build"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("RefuseBoundVariableConflicts = %v, want it refused naming %q", err, want)
		}
	}
}

func TestAValueIsNeverSetByHandForABoundVariable(t *testing.T) {
	t.Parallel()

	err := variables.RefuseBound(boundScope(buildoutput.FrameworkNode), "ORIGIN_KEY")
	for _, want := range []string{"ORIGIN_KEY", "@origin.key", "bindings.variables"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("RefuseBound = %v, want it refused naming %q", err, want)
		}
	}
	if err := variables.RefuseBound(boundScope(buildoutput.FrameworkNode), "API_TOKEN"); err != nil {
		t.Errorf("RefuseBound(API_TOKEN) = %v, want a variable no record serves left settable", err)
	}
}
