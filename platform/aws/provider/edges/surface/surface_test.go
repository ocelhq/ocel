package surface_test

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/aws/provider/account"
	"github.com/ocelhq/ocel/platform/aws/provider/edges/surface"
)

var longest = account.Namespace(strings.Repeat("a", provider.MaxNamespaceLength))

func TestANameIsTheNamespaceFieldTheTierWordTheSlugAndTheHashOfAll(t *testing.T) {
	if got, want := must(surface.Name("ocel", "shop", environment.TierProduction)), "ocel-prod-shop-"+naming.HashTag("ocel", "production", "shop"); got != want {
		t.Errorf("Name = %q, want %q", got, want)
	}
	if got, want := must(surface.Name("j-1-a", "shop", environment.TierPreview, "pr-7")), "j-1-a-preview-shop-pr-7-"+naming.HashTag("j-1-a", "preview", "shop", "pr-7"); got != want {
		t.Errorf("Name = %q, want %q", got, want)
	}
}

func TestTheLongestSlugThatFitsWholeIsNamedAndOneLongerIsRefused(t *testing.T) {
	head := naming.NamespaceField(string(longest)) + "-preview-"
	fits := strings.Repeat("s", surface.MaxNameLen-len(head)-len("-000000"))

	name, err := surface.Name(longest, fits, environment.TierPreview)
	if err != nil || len(name) != surface.MaxNameLen || !strings.HasPrefix(name, head+fits+"-") {
		t.Errorf("Name = %q, %v, want the whole slug under %q in exactly %d characters", name, err, head, surface.MaxNameLen)
	}
	var refused refusal.Refusal
	if name, err := surface.Name(longest, fits+"s", environment.TierPreview); !errors.As(err, &refused) {
		t.Errorf("Name = %q, %v, want a refusal: a cut slug cannot be read back to the project that owns the name", name, err)
	}
}

func TestProjectsNamedReadsOnlyThisNamespacesSurfacesOfTheTier(t *testing.T) {
	names := []string{
		must(surface.Name("ocel", "shop", environment.TierProduction)),
		must(surface.Name("ocel", "shop", environment.TierPreview)),
		must(surface.Name("ocel", "docs", environment.TierProduction)),
		must(surface.Name("other", "till", environment.TierProduction)),
		must(surface.Name("ocel-prod", "till", environment.TierProduction)),
		"ocel-prod-bootstrap",
	}

	got := surface.ProjectsNamed("ocel", names, environment.TierProduction)
	if want := []string{"shop", "docs"}; !slices.Equal(got, want) {
		t.Errorf("ProjectsNamed = %v, want %v", got, want)
	}
}

func TestANameOfTheLimitsLengthUnderThisHeadIsNoProjectUnlessItsTagIsThisNamespaces(t *testing.T) {
	foreign := "ocel-prod-prod-" + strings.Repeat("t", surface.MaxNameLen-len("ocel-prod-prod-")-len("-1a2b3c")) + "-1a2b3c"
	wildcard := "ocel-preview-wildcard-" + strings.Repeat("w", surface.MaxNameLen-len("ocel-preview-wildcard-")-len("-1a2b3c")) + "-1a2b3c"

	if got := surface.ProjectsNamed("ocel", []string{foreign}, environment.TierProduction); len(got) != 0 {
		t.Errorf("ProjectsNamed = %v, want none: %q is namespace ocel-prod's, and reading it as ocel's refuses ocel's teardown", got, foreign)
	}
	if got := surface.ProjectsNamed("ocel", []string{wildcard}, environment.TierPreview); len(got) != 0 {
		t.Errorf("ProjectsNamed = %v, want none: %q is the shared preview wildcard distribution, no project's own", got, wildcard)
	}
}

func must[T any](value T, err error) T {
	if err != nil {
		panic(err)
	}
	return value
}
