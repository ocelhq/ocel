package envgate_test

import (
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/envgate"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
)

var infisicalSource = envgate.Source{
	ID:    "infisical:p-1/prod",
	Links: map[string]string{"": "https://infisical.example/root", "/web": "https://infisical.example/web"},
}

func TestARefusalOnATierItsSourceOwnsSaysToSetTheValueThere(t *testing.T) {
	t.Parallel()
	refusal := &envgate.Refusal{
		Problems: []*resourcesv1.VariableProblem{missing("STRIPE_KEY", "/web"), missing("DATABASE_URL", "")},
		Scope:    envgate.Scope{Source: infisicalSource},
	}
	out := refusal.Error()
	for _, want := range []string{"infisical:p-1/prod", "https://infisical.example/web", "https://infisical.example/root"} {
		if !strings.Contains(out, want) {
			t.Errorf("refusal = %q, want %q", out, want)
		}
	}
	if strings.Contains(out, "ocel env set") {
		t.Errorf("refusal = %q, want no `ocel env set` offered for a value the source owns", out)
	}
}

func TestARefusalOverAnInvalidValueASourceOwnsSaysToFixItThere(t *testing.T) {
	t.Parallel()
	refusal := &envgate.Refusal{
		Problems: []*resourcesv1.VariableProblem{invalid("API_URL", "/web", "must be a URL")},
		Scope:    envgate.Scope{Source: infisicalSource},
	}
	out := refusal.Error()
	for _, want := range []string{"must be a URL", "fix it in infisical:p-1/prod", "https://infisical.example/web"} {
		if !strings.Contains(out, want) {
			t.Errorf("refusal = %q, want %q", out, want)
		}
	}
	if strings.Contains(out, "https://infisical.example/root") {
		t.Errorf("refusal = %q, want only the folder that holds the invalid value linked", out)
	}
}

func TestARefusalOverMissingAndInvalidValuesASourceOwnsSaysToSetOrFixThem(t *testing.T) {
	t.Parallel()
	refusal := &envgate.Refusal{
		Problems: []*resourcesv1.VariableProblem{missing("DATABASE_URL", ""), invalid("API_URL", "/web", "must be a URL")},
		Scope:    envgate.Scope{Source: infisicalSource},
	}
	if out := refusal.Error(); !strings.Contains(out, "set or fix them in infisical:p-1/prod") {
		t.Errorf("refusal = %q, want both kinds named in the remedy", out)
	}
}

func TestARefusalInTheBrowserStillOffersTheUIOverASource(t *testing.T) {
	t.Parallel()
	refusal := &envgate.Refusal{
		Problems: []*resourcesv1.VariableProblem{missing("STRIPE_KEY", "")},
		Scope:    envgate.Scope{Source: infisicalSource, Browser: true},
	}
	if out := refusal.Error(); !strings.Contains(out, "ocel env ui") {
		t.Errorf("refusal = %q, want the UI offered", out)
	}
}

func TestDriftNamesWhatTheSourceHoldsAndNothingDeclares(t *testing.T) {
	t.Parallel()
	source := infisicalSource
	source.Present = []envgate.Cell{
		{Key: "DATABASE_URL"},
		{Key: "STRIPE_KEY", Folder: "/web"},
		{Key: "OLD_TOKEN", Folder: "/web"},
		{Key: "SCOPED", Folder: "/api"},
	}
	definitions := []*resourcesv1.VariableDefinition{
		{Key: "DATABASE_URL"},
		{Key: "STRIPE_KEY"},
		{Key: "SCOPED", Folders: []string{"/web"}},
	}
	warnings := envgate.Drift(definitions, source)
	if len(warnings) != 2 {
		t.Fatalf("Drift() = %q, want OLD_TOKEN and the misplaced SCOPED", warnings)
	}
	for i, want := range []string{"OLD_TOKEN", "SCOPED"} {
		if !strings.Contains(warnings[i], want) || !strings.Contains(warnings[i], "infisical:p-1/prod") {
			t.Errorf("warning %d = %q, want %s named with its source", i, warnings[i], want)
		}
	}
}
