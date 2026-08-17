package envgate_test

import (
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/envgate"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/resources/v1"
)

func secret(key string, folders ...string) *resourcesv1.VariableDefinition {
	d := def(key, resourcesv1.VariableClass_VARIABLE_CLASS_SECRET)
	d.Folders = folders
	return d
}

func TestLintEdge(t *testing.T) {
	t.Parallel()

	apps := []envgate.App{{Name: "web", Folder: "/web"}, {Name: "admin", Folder: "/admin"}}

	t.Run("a secret and an app shipping edge entries is a warning naming both", func(t *testing.T) {
		t.Parallel()
		definition := secret("STRIPE_KEY")
		definition.Source = "web/env.ts"
		warnings, err := envgate.LintEdge(
			[]*resourcesv1.VariableDefinition{definition},
			apps,
			[]string{"web"},
		)
		if err != nil {
			t.Fatalf("LintEdge: %v", err)
		}
		if len(warnings) != 1 {
			t.Fatalf("warnings = %q, want one", warnings)
		}
		for _, want := range []string{"STRIPE_KEY", "web", "nodejs", "sensitive", "web/env.ts"} {
			if !strings.Contains(warnings[0], want) {
				t.Errorf("warning = %q, want %q named", warnings[0], want)
			}
		}
		if strings.Contains(warnings[0], "admin") {
			t.Errorf("warning = %q, want the app that ships no edge entries left out", warnings[0])
		}
		if !strings.Contains(warnings[0], "web ships edge entries") {
			t.Errorf("warning = %q, want a singular verb for one app", warnings[0])
		}
	})

	t.Run("a warning per secret, not per app", func(t *testing.T) {
		t.Parallel()
		warnings, err := envgate.LintEdge(
			[]*resourcesv1.VariableDefinition{secret("STRIPE_KEY"), secret("DB_PASSWORD")},
			apps,
			[]string{"admin", "web"},
		)
		if err != nil {
			t.Fatalf("LintEdge: %v", err)
		}
		if len(warnings) != 2 {
			t.Fatalf("warnings = %q, want one per definition", warnings)
		}
		if !strings.Contains(warnings[0], "STRIPE_KEY") || !strings.Contains(warnings[1], "DB_PASSWORD") {
			t.Errorf("warnings = %q, want each keyed to its own definition", warnings)
		}
	})

	t.Run("an edge app this project does not declare fails rather than reading an empty folder", func(t *testing.T) {
		t.Parallel()
		warnings, err := envgate.LintEdge(
			[]*resourcesv1.VariableDefinition{secret("STRIPE_KEY", "/web")},
			apps,
			[]string{"storefront"},
		)
		if err == nil {
			t.Fatalf("warnings = %q, want an unknown app to fail loudly", warnings)
		}
		if !strings.Contains(err.Error(), "storefront") {
			t.Errorf("err = %v, want the unknown app named", err)
		}
	})

	t.Run("the readable classes are silent", func(t *testing.T) {
		t.Parallel()
		warnings, err := envgate.LintEdge(
			[]*resourcesv1.VariableDefinition{
				def("PUBLIC_URL", resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN),
				def("API_TOKEN", resourcesv1.VariableClass_VARIABLE_CLASS_SENSITIVE),
			},
			apps,
			[]string{"web"},
		)
		if err != nil {
			t.Fatalf("LintEdge: %v", err)
		}
		if len(warnings) != 0 {
			t.Errorf("warnings = %q, want nothing said about a class the edge can read", warnings)
		}
	})

	t.Run("a secret is silent when no app ships edge entries", func(t *testing.T) {
		t.Parallel()
		warnings, err := envgate.LintEdge(
			[]*resourcesv1.VariableDefinition{secret("STRIPE_KEY")},
			apps,
			nil,
		)
		if err != nil {
			t.Fatalf("LintEdge: %v", err)
		}
		if len(warnings) != 0 {
			t.Errorf("warnings = %q, want nothing said without an edge entry to read from", warnings)
		}
	})

	t.Run("a secret out of the edge app's folder scope is silent", func(t *testing.T) {
		t.Parallel()
		warnings, err := envgate.LintEdge(
			[]*resourcesv1.VariableDefinition{secret("STRIPE_KEY", "/admin")},
			apps,
			[]string{"web"},
		)
		if err != nil {
			t.Fatalf("LintEdge: %v", err)
		}
		if len(warnings) != 0 {
			t.Errorf("warnings = %q, want nothing said about a secret the edge app cannot read", warnings)
		}
	})

	t.Run("names every edge app that reads the secret", func(t *testing.T) {
		t.Parallel()
		warnings, err := envgate.LintEdge(
			[]*resourcesv1.VariableDefinition{secret("STRIPE_KEY")},
			apps,
			[]string{"admin", "web"},
		)
		if err != nil {
			t.Fatalf("LintEdge: %v", err)
		}
		if len(warnings) != 1 || !strings.Contains(warnings[0], "admin and web ship edge entries") {
			t.Errorf("warnings = %q, want both edge apps named with a plural verb", warnings)
		}
	})
}
