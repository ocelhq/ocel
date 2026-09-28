package surface_test

import (
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/bootstrap"
	"github.com/ocelhq/ocel/platform/aws/provider/edges/surface"
)

const commentLimit = 128

var longest = bootstrap.Namespace(strings.Repeat("a", provider.MaxNamespaceLength))

func TestNameContainsTheNamespaceTheSlugAndTheTier(t *testing.T) {
	if got, want := surface.Name("ocel", "shop", environment.TierProduction), "ocel--shop--production"; got != want {
		t.Errorf("Name = %q, want %q", got, want)
	}
	if got, want := surface.Name("j-1-a", "shop", environment.TierPreview, "pr-7"), "j-1-a--shop--preview--pr-7"; got != want {
		t.Errorf("Name = %q, want %q", got, want)
	}
}

func TestFittedKeepsTheFieldsAGateReadsWhole(t *testing.T) {
	slug := strings.Repeat("s", 200)

	name := surface.Fitted(commentLimit, longest, slug, environment.TierProduction)
	if len(name) > commentLimit {
		t.Fatalf("Fitted = %q, which is %d characters and the limit is %d", name, len(name), commentLimit)
	}
	fields := strings.Split(name, naming.FieldSeparator)
	if len(fields) != 3 {
		t.Fatalf("Fitted = %q, which splits into %d fields; a gate reads three", name, len(fields))
	}
	if fields[0] != string(longest) {
		t.Errorf("the namespace field is %q, want %q", fields[0], longest)
	}
	if fields[2] != string(environment.TierProduction) {
		t.Errorf("the tier field is %q, want %q", fields[2], environment.TierProduction)
	}
}

func TestFittedGivesTwoLongProjectsTwoNames(t *testing.T) {
	one := surface.Fitted(commentLimit, longest, strings.Repeat("s", 200)+"-one", environment.TierProduction)
	two := surface.Fitted(commentLimit, longest, strings.Repeat("s", 200)+"-two", environment.TierProduction)
	if one == two {
		t.Errorf("two projects both mint %q, so each would adopt the other's surface", one)
	}
}

func TestFittedLeavesAShortNameAlone(t *testing.T) {
	if got, want := surface.Fitted(commentLimit, "ocel", "shop", environment.TierProduction), surface.Name("ocel", "shop", environment.TierProduction); got != want {
		t.Errorf("Fitted = %q, want %q", got, want)
	}
}

func TestProjectsNamedReadsOnlyThisNamespacesSurfaces(t *testing.T) {
	names := []string{
		surface.Name("ocel", "shop", environment.TierProduction),
		surface.Name("ocel", "shop", environment.TierPreview),
		surface.Name("ocel", "docs", environment.TierProduction, "pr-7"),
		surface.Name("other", "till", environment.TierProduction),
		"ocel-bootstrap",
	}

	got := surface.ProjectsNamed("ocel", names, environment.TierProduction)
	if len(got) != 2 || got[0] != "shop" || got[1] != "docs" {
		t.Errorf("ProjectsNamed = %v, want the production projects of the ocel namespace", got)
	}
}
