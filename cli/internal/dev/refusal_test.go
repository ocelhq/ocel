package dev

import (
	"reflect"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/dotfile"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/variables"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
)

func TestDescribeRefusal(t *testing.T) {
	t.Run("it names the dotfile rather than a store command", func(t *testing.T) {
		refusal := &variables.MissingError{
			Problems: []*resourcesv1.VariableProblem{
				{Key: "DATABASE_URL", Kind: resourcesv1.VariableProblem_KIND_MISSING},
				{Key: "API_BASE", Folder: "/web", Kind: resourcesv1.VariableProblem_KIND_INVALID, Detail: "expected a URL"},
			},
			Scope: variables.Scope{Apps: []variables.App{{Name: "web", Folder: "/web"}}},
		}

		got := describeRefusal(refusal, nil, invocation{name: "dev", source: valueSource{id: "dotenv"}}).Error()

		for _, want := range []string{
			"DATABASE_URL",
			"API_BASE",
			"/web",
			"no value is set",
			"expected a URL",
			"DATABASE_URL=<VALUE>",
			dotfile.FileName,
		} {
			if !strings.Contains(got, want) {
				t.Errorf("refusal = %q, want it to mention %q", got, want)
			}
		}
		if strings.Contains(got, "ocel env set") {
			t.Errorf("refusal = %q, want no `ocel env set`: it needs a cloud provider this path deliberately has none of", got)
		}
		if strings.Contains(got, "--preview") {
			t.Errorf("refusal = %q, want nothing about previews in a dev refusal", got)
		}
	})

	t.Run("under another dev source it names that source and "+dotfile.LocalFileName, func(t *testing.T) {
		refusal := &variables.MissingError{Problems: []*resourcesv1.VariableProblem{{Key: "DATABASE_URL", Kind: resourcesv1.VariableProblem_KIND_MISSING}}}

		got := describeRefusal(refusal, nil, invocation{name: "dev", source: valueSource{id: "infisical:p-1/dev", values: map[string]string{}}}).Error()

		for _, want := range []string{"set DATABASE_URL in infisical:p-1/dev", "DATABASE_URL=<VALUE> to " + dotfile.LocalFileName, "Set the values above in infisical:p-1/dev and " + dotfile.LocalFileName} {
			if !strings.Contains(got, want) {
				t.Errorf("refusal = %q, want it to say %q", got, want)
			}
		}
	})

	t.Run("it says so when the key is only in the shell", func(t *testing.T) {
		t.Setenv("DATABASE_URL", "postgres://from-the-shell")

		refusal := &variables.MissingError{
			Problems: []*resourcesv1.VariableProblem{
				{Key: "DATABASE_URL", Kind: resourcesv1.VariableProblem_KIND_MISSING},
			},
		}

		got := describeRefusal(refusal, nil, invocation{name: "dev", source: valueSource{id: "dotenv"}}).Error()

		if !strings.Contains(got, "shell") {
			t.Errorf("refusal = %q, want it to say the key was seen in the environment", got)
		}
		if strings.Contains(got, "postgres://from-the-shell") {
			t.Errorf("refusal = %q, want it to disclose no value", got)
		}

		inFile := describeRefusal(refusal, dotfileValues(map[string]string{"DATABASE_URL": "postgres://from-the-file"}).keys(), invocation{name: "dev", source: valueSource{id: "dotenv"}}).Error()
		if strings.Contains(inFile, "set in this shell") {
			t.Errorf("refusal = %q, want no shell hint for a key the file does contain", inFile)
		}
	})

	t.Run("an invalid value the declarations kept no detail for ends at its schema", func(t *testing.T) {
		refusal := &variables.MissingError{Problems: []*resourcesv1.VariableProblem{{Key: "API_TOKEN", Kind: resourcesv1.VariableProblem_KIND_INVALID}}}

		got := describeRefusal(refusal, nil, invocation{name: "dev", source: valueSource{id: "dotenv"}}).Error()

		if !strings.Contains(got, "set, but it does not satisfy its schema\n") {
			t.Errorf("refusal = %q, want the reason to end at the schema with nothing trailing", got)
		}
	})

	t.Run("it is never given a value it could print", func(t *testing.T) {
		want := reflect.TypeOf(func(error, map[string]struct{}, invocation) error { return nil })
		if got := reflect.TypeOf(describeRefusal); got != want {
			t.Fatalf("describeRefusal is %s, want %s: any wider parameter puts a dotfile value in reach of the message", got, want)
		}

		refusal := &variables.MissingError{
			Problems: []*resourcesv1.VariableProblem{
				{Key: "DATABASE_URL", Kind: resourcesv1.VariableProblem_KIND_MISSING},
				{Key: "API_TOKEN", Kind: resourcesv1.VariableProblem_KIND_INVALID, Detail: "expected a token"},
			},
		}
		fileValues := map[string]string{
			"DATABASE_URL": "postgres://must-not-appear",
			"API_TOKEN":    "sk-live-must-not-appear",
		}

		got := describeRefusal(refusal, dotfileValues(fileValues).keys(), invocation{name: "dev", source: valueSource{id: "dotenv"}}).Error()

		for _, value := range fileValues {
			if strings.Contains(got, value) {
				t.Errorf("refusal = %q, want it to disclose no value from %s", got, dotfile.FileName)
			}
		}
	})
}

func TestRefusalsNameTheCommandThatRan(t *testing.T) {
	refusal := &variables.MissingError{
		Problems: []*resourcesv1.VariableProblem{
			{Key: "DATABASE_URL", Kind: resourcesv1.VariableProblem_KIND_MISSING},
		},
	}

	t.Run("a variable refusal names the command that was run", func(t *testing.T) {
		t.Setenv("DATABASE_URL", "postgres://from-the-shell")

		got := describeRefusal(refusal, nil, invocation{name: "run", source: valueSource{id: "dotenv"}}).Error()

		if !strings.Contains(got, "`ocel run` again") {
			t.Errorf("refusal = %q, want it to name the command that was run", got)
		}
		if strings.Contains(got, "`ocel dev`") {
			t.Errorf("refusal = %q, want no mention of a command or flag this run never used", got)
		}
	})
}

func TestADevRunRefusesAScopedValueNoChildItStartsCouldRead(t *testing.T) {
	t.Parallel()

	apps := []project.App{
		{Name: "web", Path: "apps/web", Folder: "/web"},
		{Name: "api", Path: "apps/api", Folder: "/api"},
	}

	t.Run("it refuses when the apps do not agree on one", func(t *testing.T) {
		t.Parallel()

		if err := refuseUnstatableBinding(valueSource{id: "dotenv"}, apps, "", project.DefaultFileName, nil); err != nil {
			t.Errorf("refuseUnstatableBinding = %v, want nil with no scoped variable declared", err)
		}

		agreed := []project.App{{Name: "web", Folder: "/web"}, {Name: "admin", Folder: "/web"}}
		if err := refuseUnstatableBinding(valueSource{id: "dotenv"}, agreed, "/web", project.DefaultFileName, map[string][]string{"API_BASE": {"/web"}}); err != nil {
			t.Errorf("refuseUnstatableBinding = %v, want nil when every app binds the folder dev states", err)
		}

		err := refuseUnstatableBinding(valueSource{id: "dotenv"}, apps, "", project.DefaultFileName, map[string][]string{"API_BASE": {"/web", "/api"}})
		if err == nil {
			t.Fatal("refuseUnstatableBinding = nil, want a refusal: no child of this run could read API_BASE")
		}
		got := err.Error()
		for _, want := range []string{"API_BASE", "web binds /web", "api binds /api", "the project root", dotfile.FileName, "ocel.json"} {
			if !strings.Contains(got, want) {
				t.Errorf("refusal = %q, want it to mention %q", got, want)
			}
		}
		if strings.Contains(got, "one `ocel dev` per app") || strings.Contains(got, "per app") {
			t.Errorf("refusal = %q, want no per-app remedy: election keys on the project root, so a second run is a follower", got)
		}
	})

	t.Run("it starts when no app binds the key's scope", func(t *testing.T) {
		t.Parallel()

		if err := refuseUnstatableBinding(valueSource{id: "dotenv"}, apps, "", project.DefaultFileName, map[string][]string{"NOBODY": {"/nowhere"}}); err != nil {
			t.Errorf("refuseUnstatableBinding = %v, want nil: no app binds /nowhere, so no read is lost", err)
		}

		scoped := map[string][]string{"NOBODY": {"/nowhere"}, "API_BASE": {"/web"}}
		err := refuseUnstatableBinding(valueSource{id: "dotenv"}, apps, "", project.DefaultFileName, scoped)
		if err == nil {
			t.Fatal("refuseUnstatableBinding = nil, want a refusal for API_BASE, which web would read under its own binding")
		}
		if strings.Contains(err.Error(), "NOBODY") {
			t.Errorf("refusal = %q, want it silent about NOBODY: naming a key no app binds sends the developer after nothing", err.Error())
		}
	})

	t.Run("it names where the dev tier reads its values from", func(t *testing.T) {
		t.Parallel()

		err := refuseUnstatableBinding(valueSource{id: "infisical:p-1/dev"}, apps, "", project.DefaultFileName, map[string][]string{"API_BASE": {"/web"}})
		if err == nil {
			t.Fatal("refuseUnstatableBinding = nil, want a refusal: web would read API_BASE under its own binding")
		}
		if got := err.Error(); !strings.Contains(got, "even with the value in infisical:p-1/dev and .env.local") {
			t.Errorf("refusal = %q, want it to name the dev env source and .env.local, never .env alone", got)
		}
	})

	t.Run("it names only the apps binding the key's scope", func(t *testing.T) {
		t.Parallel()

		err := refuseUnstatableBinding(valueSource{id: "dotenv"}, apps, "", project.DefaultFileName, map[string][]string{"API_BASE": {"/web"}})
		if err == nil {
			t.Fatal("refuseUnstatableBinding = nil, want a refusal: web would read API_BASE under its own binding")
		}
		got := err.Error()
		if !strings.Contains(got, "API_BASE") || !strings.Contains(got, "web binds /web") {
			t.Errorf("refusal = %q, want it to name API_BASE and web's binding", got)
		}
		if strings.Contains(got, "api binds /api") {
			t.Errorf("refusal = %q, want no mention of api: /api is not in API_BASE's scope", got)
		}
	})

	t.Run("it lists every losing key and app in a fixed order", func(t *testing.T) {
		t.Parallel()

		scoped := map[string][]string{
			"D_KEY": {"/api"},
			"B_KEY": {"/api"},
			"C_KEY": {"/web"},
			"A_KEY": {"/web"},
		}

		want := "A_KEY, B_KEY, C_KEY, D_KEY are scoped to a folder this run cannot state — the app has not been started.\n" +
			"\n  web binds /web\n  api binds /api\n\n" +
			"`ocel dev` and `ocel run` spawn one child for the whole project and nothing tells it which app that child is, " +
			"so the binding they state is the project root. A scoped read refuses under it, even with the value in " + dotfile.FileName + ".\n\n" +
			"fix: bind every app to the same folder in ocel.json, or drop `folders:` from those declarations"

		for range 50 {
			err := refuseUnstatableBinding(valueSource{id: "dotenv"}, apps, "", project.DefaultFileName, scoped)
			if err == nil {
				t.Fatal("refuseUnstatableBinding = nil, want a refusal: both apps lose a read")
			}
			if got := err.Error(); got != want {
				t.Fatalf("refusal =\n%q\nwant\n%q", got, want)
			}
		}
	})
}
