package clientenv

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/variables"
	"github.com/ocelhq/ocel/pkg/buildoutput"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	"github.com/ocelhq/ocel/pkg/statedir"
)

func readRecordFile(t *testing.T, root string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, statedir.Name, "output", "client-digests.json"))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func publicVar(key, value string) variables.Variable {
	return variables.Variable{Key: key, Class: resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN, Value: value}
}

func serverVar(key, value string) variables.Variable {
	return variables.Variable{Key: key, Class: resourcesv1.VariableClass_VARIABLE_CLASS_SENSITIVE, Value: value}
}

func nextApp(name string, vars ...variables.Variable) App {
	return App{Name: name, Framework: buildoutput.FrameworkNext, ClientBundle: true, Variables: vars}
}

func TestPublicKeys(t *testing.T) {
	t.Parallel()

	t.Run("names the plain NEXT_PUBLIC_ variables of a next app, sorted", func(t *testing.T) {
		t.Parallel()

		app := nextApp("web",
			publicVar("NEXT_PUBLIC_RETRIES", "3"),
			publicVar("INTERNAL_URL", "http://internal"),
			publicVar("NEXT_PUBLIC_API_URL", "https://api.example.com"),
			serverVar("STRIPE_API_KEY", "sk-live"),
		)

		got := PublicKeys(app)
		want := []string{"NEXT_PUBLIC_API_URL", "NEXT_PUBLIC_RETRIES"}
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("PublicKeys = %v, want %v", got, want)
		}
	})

	t.Run("includes the deployment url ocel writes for a next app", func(t *testing.T) {
		t.Parallel()

		got := PublicKeys(nextApp("web", publicVar("NEXT_PUBLIC_OCEL_URL", "https://web.example.com")))

		if len(got) != 1 || got[0] != "NEXT_PUBLIC_OCEL_URL" {
			t.Errorf("PublicKeys = %v, want the deployment url", got)
		}
	})

	t.Run("never names a confidential variable, whatever its prefix", func(t *testing.T) {
		t.Parallel()

		app := nextApp("web", serverVar("NEXT_PUBLIC_TOKEN", "sk-live"))

		if got := PublicKeys(app); len(got) != 0 {
			t.Errorf("PublicKeys = %v, want none: a confidential value never reaches a browser bundle", got)
		}
	})

	t.Run("names nothing for an app whose framework inlines no define", func(t *testing.T) {
		t.Parallel()

		for _, framework := range []string{buildoutput.FrameworkSvelteKit, buildoutput.FrameworkNode, buildoutput.FrameworkGo, ""} {
			app := App{Name: "web", Framework: framework, ClientBundle: true, Variables: []variables.Variable{publicVar("NEXT_PUBLIC_API_URL", "x")}}
			if got := PublicKeys(app); len(got) != 0 {
				t.Errorf("PublicKeys(%q app) = %v, want none", framework, got)
			}
		}
	})
}

func TestDeclaredPublicKeys(t *testing.T) {
	t.Parallel()

	got := DeclaredPublicKeys([]*resourcesv1.VariableDefinition{
		{Key: "NEXT_PUBLIC_RETRIES", Class: resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN},
		{Key: "INTERNAL_URL", Class: resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN},
		{Key: "NEXT_PUBLIC_API_URL", Class: resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN},
		{Key: "NEXT_PUBLIC_TOKEN", Class: resourcesv1.VariableClass_VARIABLE_CLASS_SECRET},
	})

	want := []string{"NEXT_PUBLIC_API_URL", "NEXT_PUBLIC_RETRIES"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("DeclaredPublicKeys = %v, want the plain NEXT_PUBLIC_ keys %v", got, want)
	}
}

func TestCheckFresh(t *testing.T) {
	t.Parallel()

	t.Run("refuses a bundle that predates the value it inlines", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		built := []App{{Name: "storefront", Framework: buildoutput.FrameworkNext, Variables: []variables.Variable{publicVar("NEXT_PUBLIC_SITE_URL", "https://example.com")}}}
		if err := Record(root, built); err != nil {
			t.Fatalf("Record: %v", err)
		}

		rotated := []App{{Name: "storefront", Framework: buildoutput.FrameworkNext, Variables: []variables.Variable{publicVar("NEXT_PUBLIC_SITE_URL", "https://rotated.example.com")}}}
		err := CheckFresh(root, rotated)
		if err == nil {
			t.Fatal("CheckFresh = nil for a build that predates the value, want a refusal")
		}
		if !strings.Contains(err.Error(), "NEXT_PUBLIC_SITE_URL") {
			t.Errorf("error = %q, want it to name the changed key", err)
		}
		if !strings.Contains(err.Error(), "--prebuilt") {
			t.Errorf("error = %q, want it to name the flag being refused", err)
		}
	})

	t.Run("allows a bundle built with the same values", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		built := []App{{Name: "storefront", Framework: buildoutput.FrameworkNext, Variables: []variables.Variable{
			publicVar("NEXT_PUBLIC_SITE_URL", "https://example.com"),
			serverVar("STRIPE_API_KEY", "sk-live"),
		}}}
		if err := Record(root, built); err != nil {
			t.Fatalf("Record: %v", err)
		}

		rotated := []App{{Name: "storefront", Framework: buildoutput.FrameworkNext, Variables: []variables.Variable{
			publicVar("NEXT_PUBLIC_SITE_URL", "https://example.com"),
			serverVar("STRIPE_API_KEY", "sk-rotated"),
		}}}
		if err := CheckFresh(root, rotated); err != nil {
			t.Errorf("CheckFresh = %v, want a variables-only deploy of a server value to proceed", err)
		}
	})

	t.Run("refuses an ocel build output for what it is", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		if err := Record(root, []App{{Name: "storefront", Framework: buildoutput.FrameworkNext}}); err != nil {
			t.Fatalf("Record: %v", err)
		}

		err := CheckFresh(root, []App{{Name: "storefront", Framework: buildoutput.FrameworkNext, Variables: []variables.Variable{publicVar("NEXT_PUBLIC_SITE_URL", "https://example.com")}}})
		if err == nil {
			t.Fatal("CheckFresh = nil for an output that inlined no client value, want a refusal")
		}
		for _, want := range []string{"NEXT_PUBLIC_SITE_URL", "never inlined", "`ocel build`, which resolves no values"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error = %q, want it to state %q", err, want)
			}
		}
		if strings.Contains(err.Error(), "changed since") {
			t.Errorf("error = %q, want it not to claim the value changed; it was never inlined", err)
		}
	})

	t.Run("refuses a key a build never knew about", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		if err := Record(root, []App{{Name: "storefront", Framework: buildoutput.FrameworkNext, Variables: []variables.Variable{serverVar("STRIPE_API_KEY", "sk-live")}}}); err != nil {
			t.Fatalf("Record: %v", err)
		}

		err := CheckFresh(root, []App{{Name: "storefront", Framework: buildoutput.FrameworkNext, Variables: []variables.Variable{publicVar("NEXT_PUBLIC_SITE_URL", "https://example.com")}}})
		if err == nil {
			t.Fatal("CheckFresh = nil for a key the build never inlined, want a refusal")
		}
		for _, want := range []string{"NEXT_PUBLIC_SITE_URL", "never inlined", "not a public variable when"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error = %q, want it to state %q", err, want)
			}
		}
	})

	t.Run("says nothing about a project with no client values", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()

		apps := []App{{Name: "storefront", Framework: buildoutput.FrameworkNext, Variables: []variables.Variable{serverVar("STRIPE_API_KEY", "sk-live")}}}
		if err := CheckFresh(root, apps); err != nil {
			t.Errorf("CheckFresh = %v, want nothing to check", err)
		}
	})
}

func TestRecord(t *testing.T) {
	t.Parallel()

	t.Run("stores no plaintext", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()

		err := Record(root, []App{{Name: "storefront", Framework: buildoutput.FrameworkNext, Variables: []variables.Variable{publicVar("NEXT_PUBLIC_SITE_URL", "https://example.com")}}})
		if err != nil {
			t.Fatalf("Record: %v", err)
		}

		if got := readRecordFile(t, root); strings.Contains(got, "https://example.com") {
			t.Errorf("record contains the value itself: %s", got)
		}
	})
}
