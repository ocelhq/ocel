package surface_test

import (
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/account"
	"github.com/ocelhq/ocel/platform/aws/provider/edges/surface"
)

var longest = account.Namespace(strings.Repeat("a", provider.MaxNamespaceLength))

func TestANameIsTheNamespaceFieldTheTierWordTheSlugAndTheHashOfAll(t *testing.T) {
	if got, want := surface.Name("ocel", "shop", environment.TierProduction), "ocel-prod-shop-"+naming.HashTag("ocel", "production", "shop"); got != want {
		t.Errorf("Name = %q, want %q", got, want)
	}
	if got, want := surface.Name("j-1-a", "shop", environment.TierPreview, "pr-7"), "j-1-a-preview-shop-pr-7-"+naming.HashTag("j-1-a", "preview", "shop", "pr-7"); got != want {
		t.Errorf("Name = %q, want %q", got, want)
	}
}

func TestALongSlugIsCutToTheLimitAndKeepsItsHash(t *testing.T) {
	slug := strings.Repeat("s", 200)

	name := surface.Name(longest, slug, environment.TierProduction)
	if len(name) > surface.MaxNameLen {
		t.Fatalf("Name = %q, which is %d characters and the limit is %d", name, len(name), surface.MaxNameLen)
	}
	if head := naming.NamespaceField(string(longest)) + "-prod-"; !strings.HasPrefix(name, head) {
		t.Errorf("Name = %q, want it under %q", name, head)
	}
	if tag := naming.HashTag(string(longest), "production", slug); !strings.HasSuffix(name, "-"+tag) {
		t.Errorf("Name = %q, want it to end in the hash %q", name, tag)
	}
}

func TestTwoLongProjectsGetTwoNames(t *testing.T) {
	one := surface.Name(longest, strings.Repeat("s", 200)+"-one", environment.TierProduction)
	two := surface.Name(longest, strings.Repeat("s", 200)+"-two", environment.TierProduction)
	if one == two {
		t.Errorf("two projects both mint %q, so each would adopt the other's surface", one)
	}
}

func TestProjectsNamedReadsOnlyThisNamespacesSurfacesOfTheTier(t *testing.T) {
	names := []string{
		surface.Name("ocel", "shop", environment.TierProduction),
		surface.Name("ocel", "shop", environment.TierPreview),
		surface.Name("ocel", "docs", environment.TierProduction),
		surface.Name("other", "till", environment.TierProduction),
		surface.Name("ocel-prod", "till", environment.TierProduction),
		surface.Name("ocel", strings.Repeat("l", 200), environment.TierProduction),
		"ocel-prod-bootstrap",
	}

	got := surface.ProjectsNamed("ocel", names, environment.TierProduction)
	want := []string{"shop", "docs", strings.Repeat("l", 128-len("ocel-prod-")-len("-000000"))}
	if len(got) != len(want) {
		t.Fatalf("ProjectsNamed = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("ProjectsNamed[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}
