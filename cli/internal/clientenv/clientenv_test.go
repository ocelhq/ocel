package clientenv

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/variables"
	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/processenv"
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

func plainDefinition(key string, folders ...string) *resourcesv1.VariableDefinition {
	return &resourcesv1.VariableDefinition{Key: key, Class: resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN, Folders: folders}
}

func nextApp(name string, vars ...variables.Variable) App {
	app := App{App: variables.App{Name: name, Framework: buildoutput.FrameworkNext}, Variables: vars}
	for _, v := range vars {
		app.Declared = append(app.Declared, &resourcesv1.VariableDefinition{Key: v.Key, Class: v.Class})
	}
	return app
}

func kitApp(name string, vars ...variables.Variable) App {
	app := nextApp(name, vars...)
	app.Framework = buildoutput.FrameworkSvelteKit
	return app
}

func TestANextAppIsHandedTheKeysOfThePlainNextPublicVariablesDeclaredForIt(t *testing.T) {
	t.Parallel()

	web := variables.App{Name: "web", Folder: "/web", Framework: buildoutput.FrameworkNext}
	definitions := []*resourcesv1.VariableDefinition{
		plainDefinition("NEXT_PUBLIC_RETRIES"),
		plainDefinition("INTERNAL_URL"),
		plainDefinition("NEXT_PUBLIC_API_URL", "/web"),
		plainDefinition("NEXT_PUBLIC_ADMIN_URL", "/admin"),
		{Key: "NEXT_PUBLIC_TOKEN", Class: resourcesv1.VariableClass_VARIABLE_CLASS_SECRET},
	}

	got := PublicKeys(web, definitions)

	want := []string{"NEXT_PUBLIC_API_URL", processenv.NextPublicURLEnvVar, "NEXT_PUBLIC_RETRIES"}
	if !slices.Equal(got, want) {
		t.Errorf("PublicKeys = %v, want %v: the deployment url and the plain NEXT_PUBLIC_ keys declared for every app or for /web, never another app's or a confidential one", got, want)
	}
}

func TestAnAppOfAnotherFrameworkIsHandedNoPublicKeys(t *testing.T) {
	t.Parallel()

	for _, framework := range []string{buildoutput.FrameworkSvelteKit, buildoutput.FrameworkNode, buildoutput.FrameworkGo, ""} {
		app := variables.App{Name: "web", Framework: framework}
		if got := PublicKeys(app, []*resourcesv1.VariableDefinition{plainDefinition("NEXT_PUBLIC_API_URL")}); len(got) != 0 {
			t.Errorf("PublicKeys(%q app) = %v, want none: only next reads ocel's define", framework, got)
		}
	}
}

func TestAppsOfHandsEachAppOnlyTheDeclarationsThatReachIt(t *testing.T) {
	t.Parallel()

	cfg := &project.Project{Apps: []project.App{
		{Name: "web", Folder: "/web", Serverless: &project.Serverless{Framework: buildoutput.FrameworkNext}},
		{Name: "admin", Folder: "/admin", Serverless: &project.Serverless{Framework: buildoutput.FrameworkNext}},
	}}
	definitions := []*resourcesv1.VariableDefinition{plainDefinition("NEXT_PUBLIC_SHARED"), plainDefinition("NEXT_PUBLIC_ADMIN_URL", "/admin")}

	byName := map[string][]string{}
	for _, app := range AppsOf(cfg, definitions, nil) {
		byName[app.Name] = PublicKeys(app.App, app.Declared)
	}

	if want := []string{processenv.NextPublicURLEnvVar, "NEXT_PUBLIC_SHARED"}; !slices.Equal(byName["web"], want) {
		t.Errorf("web public keys = %v, want %v", byName["web"], want)
	}
	if want := []string{"NEXT_PUBLIC_ADMIN_URL", processenv.NextPublicURLEnvVar, "NEXT_PUBLIC_SHARED"}; !slices.Equal(byName["admin"], want) {
		t.Errorf("admin public keys = %v, want %v", byName["admin"], want)
	}
}

func TestCheckFresh(t *testing.T) {
	t.Parallel()

	t.Run("refuses a bundle that predates the value it inlines", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		built := []App{nextApp("storefront", publicVar("NEXT_PUBLIC_SITE_URL", "https://example.com"))}
		if err := Record(root, built); err != nil {
			t.Fatalf("Record: %v", err)
		}

		rotated := []App{nextApp("storefront", publicVar("NEXT_PUBLIC_SITE_URL", "https://rotated.example.com"))}
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
		built := []App{nextApp("storefront", publicVar("NEXT_PUBLIC_SITE_URL", "https://example.com"),
			serverVar("STRIPE_API_KEY", "sk-live"))}
		if err := Record(root, built); err != nil {
			t.Fatalf("Record: %v", err)
		}

		rotated := []App{nextApp("storefront", publicVar("NEXT_PUBLIC_SITE_URL", "https://example.com"),
			serverVar("STRIPE_API_KEY", "sk-rotated"))}
		if err := CheckFresh(root, rotated); err != nil {
			t.Errorf("CheckFresh = %v, want a variables-only deploy of a server value to proceed", err)
		}
	})

	t.Run("refuses an ocel build output for what it is", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		if err := Record(root, []App{nextApp("storefront")}); err != nil {
			t.Fatalf("Record: %v", err)
		}

		err := CheckFresh(root, []App{nextApp("storefront", publicVar("NEXT_PUBLIC_SITE_URL", "https://example.com"))})
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
		if err := Record(root, []App{nextApp("storefront", serverVar("STRIPE_API_KEY", "sk-live"))}); err != nil {
			t.Fatalf("Record: %v", err)
		}

		err := CheckFresh(root, []App{nextApp("storefront", publicVar("NEXT_PUBLIC_SITE_URL", "https://example.com"))})
		if err == nil {
			t.Fatal("CheckFresh = nil for a key the build never inlined, want a refusal")
		}
		for _, want := range []string{"NEXT_PUBLIC_SITE_URL", "never inlined", "not a variable the app inlines when"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error = %q, want it to state %q", err, want)
			}
		}
	})

	t.Run("refuses a bundle that inlined a value this deploy no longer has", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		if err := Record(root, []App{nextApp("storefront", publicVar("NEXT_PUBLIC_THEME", "dark"))}); err != nil {
			t.Fatalf("Record: %v", err)
		}

		unset := nextApp("storefront")
		unset.Declared = []*resourcesv1.VariableDefinition{plainDefinition("NEXT_PUBLIC_THEME")}
		err := CheckFresh(root, []App{unset})
		if err == nil {
			t.Fatal("CheckFresh = nil for a bundle holding a value the deploy unset, want a refusal")
		}
		if !strings.Contains(err.Error(), "NEXT_PUBLIC_THEME") {
			t.Errorf("error = %q, want it to name the unset key", err)
		}
	})

	t.Run("refuses a bundle that inlined a value of a key no longer declared", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		if err := Record(root, []App{nextApp("storefront", publicVar("NEXT_PUBLIC_THEME", "dark"))}); err != nil {
			t.Fatalf("Record: %v", err)
		}

		err := CheckFresh(root, []App{nextApp("storefront")})
		if err == nil {
			t.Fatal("CheckFresh = nil for a bundle holding a value of a key the deploy no longer declares, want a refusal")
		}
		if !strings.Contains(err.Error(), "NEXT_PUBLIC_THEME") {
			t.Errorf("error = %q, want it to name the undeclared key", err)
		}
	})

	t.Run("allows a bundle whose unset key is no longer declared", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		unset := nextApp("storefront")
		unset.Declared = []*resourcesv1.VariableDefinition{plainDefinition("NEXT_PUBLIC_THEME")}
		if err := Record(root, []App{unset}); err != nil {
			t.Fatalf("Record: %v", err)
		}

		if err := CheckFresh(root, []App{nextApp("storefront")}); err != nil {
			t.Errorf("CheckFresh = %v, want it to proceed: the bundle never inlined a value for the key", err)
		}
	})

	t.Run("allows a bundle built and deployed without a value for a declared key", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		unset := nextApp("storefront")
		unset.Declared = []*resourcesv1.VariableDefinition{plainDefinition("NEXT_PUBLIC_THEME")}
		if err := Record(root, []App{unset}); err != nil {
			t.Fatalf("Record: %v", err)
		}

		if err := CheckFresh(root, []App{unset}); err != nil {
			t.Errorf("CheckFresh = %v, want it to proceed: nothing was inlined and nothing is resolved", err)
		}
	})

	t.Run("refuses a bundle built without a value this deploy resolves", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		unset := nextApp("storefront")
		unset.Declared = []*resourcesv1.VariableDefinition{plainDefinition("NEXT_PUBLIC_THEME")}
		if err := Record(root, []App{unset}); err != nil {
			t.Fatalf("Record: %v", err)
		}

		err := CheckFresh(root, []App{nextApp("storefront", publicVar("NEXT_PUBLIC_THEME", "dark"))})
		if err == nil {
			t.Fatal("CheckFresh = nil for a bundle built without the value, want a refusal")
		}
		for _, want := range []string{"NEXT_PUBLIC_THEME", "`ocel build`, which resolves no values"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error = %q, want it to state %q", err, want)
			}
		}
	})

	t.Run("allows a key declared since the build that still has no value", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		if err := Record(root, []App{nextApp("storefront")}); err != nil {
			t.Fatalf("Record: %v", err)
		}

		declared := nextApp("storefront")
		declared.Declared = []*resourcesv1.VariableDefinition{plainDefinition("NEXT_PUBLIC_THEME")}
		if err := CheckFresh(root, []App{declared}); err != nil {
			t.Errorf("CheckFresh = %v, want it to proceed: no build or deploy ever had a value to inline", err)
		}
	})

	t.Run("refuses a sveltekit bundle that predates any plain value it may have inlined", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		if err := Record(root, []App{kitApp("shop", publicVar("PUBLIC_API_URL", "https://api.example.com"), publicVar("REGION", "eu-west-1"))}); err != nil {
			t.Fatalf("Record: %v", err)
		}

		err := CheckFresh(root, []App{kitApp("shop", publicVar("PUBLIC_API_URL", "https://api.example.com"), publicVar("REGION", "us-east-1"))})
		if err == nil {
			t.Fatal("CheckFresh = nil for a sveltekit build whose plain value changed, want a refusal: sveltekit inlines a static value, and which are static only sveltekit knows")
		}
		if !strings.Contains(err.Error(), "REGION") || strings.Contains(err.Error(), "PUBLIC_API_URL") {
			t.Errorf("error = %q, want it to name REGION, the one value that changed", err)
		}
	})

	t.Run("allows a sveltekit bundle whose sensitive value rotated", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		if err := Record(root, []App{kitApp("shop", publicVar("PUBLIC_API_URL", "https://api.example.com"), serverVar("STRIPE_API_KEY", "sk-live"))}); err != nil {
			t.Fatalf("Record: %v", err)
		}

		if err := CheckFresh(root, []App{kitApp("shop", publicVar("PUBLIC_API_URL", "https://api.example.com"), serverVar("STRIPE_API_KEY", "sk-rotated"))}); err != nil {
			t.Errorf("CheckFresh = %v, want it to proceed: a sensitive value is never static, so no bundle holds it", err)
		}
	})

	t.Run("says nothing about a project with no client values", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()

		apps := []App{nextApp("storefront", serverVar("STRIPE_API_KEY", "sk-live"))}
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

		err := Record(root, []App{nextApp("storefront", publicVar("NEXT_PUBLIC_SITE_URL", "https://example.com"))})
		if err != nil {
			t.Fatalf("Record: %v", err)
		}

		if got := readRecordFile(t, root); strings.Contains(got, "https://example.com") {
			t.Errorf("record contains the value itself: %s", got)
		}
	})
}
