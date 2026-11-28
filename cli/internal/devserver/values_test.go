package devserver

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/variables"
	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/processenv"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	"github.com/ocelhq/ocel/pkg/proto/app/resources/v1/resourcesv1connect"
)

func declareEnv(t *testing.T, url string, definitions ...*resourcesv1.VariableDefinition) *resourcesv1.DeclareEnvResponse {
	t.Helper()
	client := resourcesv1connect.NewResourceServiceClient(testClient, url)
	resp, err := client.DeclareEnv(context.Background(), &resourcesv1.DeclareEnvRequest{Definitions: definitions})
	if err != nil {
		t.Fatalf("DeclareEnv: %v", err)
	}
	return resp
}

func reportProblems(t *testing.T, url string, problems ...*resourcesv1.VariableProblem) {
	t.Helper()
	client := resourcesv1connect.NewResourceServiceClient(testClient, url)
	if _, err := client.ReportEnvProblems(context.Background(), &resourcesv1.ReportEnvProblemsRequest{Problems: problems}); err != nil {
		t.Fatalf("ReportEnvProblems: %v", err)
	}
}

func serveValues(t *testing.T, values map[string]string, scope variables.Scope) (*Server, string) {
	t.Helper()
	s := newDevServer(&fakeResources{})
	if values != nil {
		s.UseValues(values, scope)
	}
	return s, serve(t, s)
}

func TestDeclareEnv(t *testing.T) {
	t.Parallel()

	t.Run("answers a root key with its plaintext", func(t *testing.T) {
		t.Parallel()
		s, url := serveValues(t, map[string]string{"DATABASE_URL": "postgres://localhost/app"}, variables.Scope{})

		msg := declareEnv(t, url, &resourcesv1.VariableDefinition{
			Key: "DATABASE_URL", Class: resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN, Required: true,
		})

		cells := msg.GetCells()
		if len(cells) != 1 {
			t.Fatalf("cells = %v, want exactly one", cells)
		}
		if cells[0].GetFolder() != "" {
			t.Errorf("folder = %q, want the project root spelled as the empty string", cells[0].GetFolder())
		}
		if cells[0].GetValue() != "postgres://localhost/app" {
			t.Errorf("value = %q, want the dotfile's plaintext", cells[0].GetValue())
		}
		if err := s.RefuseIncompleteEnv(context.Background()); err != nil {
			t.Errorf("RefuseIncompleteEnv = %v, want nil — the value is there", err)
		}
	})

	t.Run("never reveals a secret value", func(t *testing.T) {
		t.Parallel()
		_, url := serveValues(t, map[string]string{"WEBHOOK_SECRET": "whsec_must_not_appear"}, variables.Scope{})

		msg := declareEnv(t, url, &resourcesv1.VariableDefinition{
			Key: "WEBHOOK_SECRET", Class: resourcesv1.VariableClass_VARIABLE_CLASS_SECRET, Required: true,
		})

		cells := msg.GetCells()
		if len(cells) != 1 {
			t.Fatalf("cells = %v, want presence for the declared key", cells)
		}
		if cells[0].GetValue() != "" {
			t.Errorf("value = %q, want a secret cell answered as presence with no plaintext", cells[0].GetValue())
		}
	})

	t.Run("a root line broadcasts to every folder a scoped key names", func(t *testing.T) {
		t.Parallel()
		s, url := serveValues(t, map[string]string{"API_BASE": "http://localhost:3000"}, variables.Scope{
			Apps: []variables.App{{Name: "web", Folder: "/web"}, {Name: "admin", Folder: "/admin"}},
		})

		msg := declareEnv(t, url, &resourcesv1.VariableDefinition{
			Key: "API_BASE", Class: resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN, Required: true,
			Folders: []string{"/web", "/admin"},
		})

		var folders []string
		for _, cell := range msg.GetCells() {
			folders = append(folders, cell.GetFolder())
			if cell.GetValue() != "http://localhost:3000" {
				t.Errorf("cell %q value = %q, want the one root value broadcast unchanged", cell.GetFolder(), cell.GetValue())
			}
		}
		slices.Sort(folders)
		if want := []string{"/admin", "/web"}; !slices.Equal(folders, want) {
			t.Fatalf("folders = %v, want one cell per folder in the scope", folders)
		}
		if err := s.RefuseIncompleteEnv(context.Background()); err != nil {
			t.Errorf("RefuseIncompleteEnv = %v, want nil — the broadcast covers every required cell", err)
		}
	})

	t.Run("keeps every declaration's folders for one key", func(t *testing.T) {
		t.Parallel()
		s, url := serveValues(t, map[string]string{"API_BASE": "http://localhost:3000"}, variables.Scope{
			Apps: []variables.App{{Name: "web", Folder: "/web"}, {Name: "admin", Folder: "/admin"}},
		})

		declareEnv(t, url, &resourcesv1.VariableDefinition{
			Key: "API_BASE", Class: resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN, Required: true,
			Folders: []string{"/web"},
		})
		msg := declareEnv(t, url, &resourcesv1.VariableDefinition{
			Key: "API_BASE", Class: resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN, Required: true,
			Folders: []string{"/admin"},
		})

		var folders []string
		for _, cell := range msg.GetCells() {
			folders = append(folders, cell.GetFolder())
		}
		slices.Sort(folders)
		if want := []string{"/admin", "/web"}; !slices.Equal(folders, want) {
			t.Fatalf("folders = %v, want both declarations' folders listed", folders)
		}
		if err := s.RefuseIncompleteEnv(context.Background()); err != nil {
			t.Fatalf("RefuseIncompleteEnv = %v, want nil", err)
		}
	})
}

func TestRefuseIncompleteEnv(t *testing.T) {
	t.Parallel()

	t.Run("refuses nothing when no values are installed", func(t *testing.T) {
		t.Parallel()
		s, url := serveValues(t, nil, variables.Scope{})

		msg := declareEnv(t, url, &resourcesv1.VariableDefinition{
			Key: "DATABASE_URL", Class: resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN, Required: true,
		})
		if len(msg.GetCells()) != 0 {
			t.Errorf("cells = %v, want none", msg.GetCells())
		}
		if err := s.RefuseIncompleteEnv(context.Background()); err != nil {
			t.Errorf("RefuseIncompleteEnv = %v, want nil", err)
		}
	})

	t.Run("refuses a required key the values do not contain", func(t *testing.T) {
		t.Parallel()
		s, url := serveValues(t, map[string]string{}, variables.Scope{Apps: []variables.App{{Name: "web"}}})

		declareEnv(t, url, &resourcesv1.VariableDefinition{
			Key: "DATABASE_URL", Class: resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN, Required: true,
		})

		err := s.RefuseIncompleteEnv(context.Background())
		var refusal *variables.MissingError
		if !errors.As(err, &refusal) {
			t.Fatalf("RefuseIncompleteEnv = %v (%T), want *variables.MissingError", err, err)
		}
		if len(refusal.Problems) != 1 || refusal.Problems[0].GetKey() != "DATABASE_URL" {
			t.Fatalf("problems = %v, want one naming DATABASE_URL", refusal.Problems)
		}
		if refusal.Problems[0].GetKind() != resourcesv1.VariableProblem_KIND_MISSING {
			t.Errorf("kind = %v, want KIND_MISSING", refusal.Problems[0].GetKind())
		}
	})

	t.Run("keeps the problems the declaring process reports", func(t *testing.T) {
		t.Parallel()
		s, url := serveValues(t, map[string]string{"PORT": "not-a-number"}, variables.Scope{})

		declareEnv(t, url, &resourcesv1.VariableDefinition{
			Key: "PORT", Class: resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN, Required: true,
		})
		reportProblems(t, url, &resourcesv1.VariableProblem{
			Key: "PORT", Kind: resourcesv1.VariableProblem_KIND_INVALID, Detail: "expected a number",
		})

		err := s.RefuseIncompleteEnv(context.Background())
		if err == nil || !strings.Contains(err.Error(), "PORT") {
			t.Fatalf("RefuseIncompleteEnv = %v, want a refusal naming PORT", err)
		}
	})

	t.Run("rules from the store as of the end of discovery", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()
		s := newDevServer(&fakeResources{})
		s.UseValues(map[string]string{"API_BASE": "http://localhost:3000"}, variables.Scope{
			Apps: []variables.App{{Name: "web", Folder: "/web"}},
		})

		store, declarations := s.env.current()
		if err := declarations.Prefetch(ctx); err != nil {
			t.Fatalf("Prefetch: %v", err)
		}
		definitions := []*resourcesv1.VariableDefinition{{
			Key: "API_BASE", Class: resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN, Required: true,
			Folders: []string{"/web"},
		}}
		store.Declare(definitions)
		if _, err := declarations.DeclareEnv(ctx, &resourcesv1.DeclareEnvRequest{Definitions: definitions}); err != nil {
			t.Fatalf("DeclareEnv: %v", err)
		}

		if err := s.RefuseIncompleteEnv(ctx); err != nil {
			t.Fatalf("RefuseIncompleteEnv = %v, want nil: the file has a value for every folder the declaration names", err)
		}
	})

	t.Run("refuses a secret key it has no value for", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()
		s, url := serveValues(t, map[string]string{}, variables.Scope{Apps: []variables.App{{Name: "web"}}})

		msg := declareEnv(t, url, &resourcesv1.VariableDefinition{
			Key: "DB_PASSWORD", Class: resourcesv1.VariableClass_VARIABLE_CLASS_SECRET, Required: true,
		})
		if len(msg.GetCells()) != 0 {
			t.Fatalf("cells = %v, want no presence for an unset secret", msg.GetCells())
		}

		var refusal *variables.MissingError
		if err := s.RefuseIncompleteEnv(ctx); !errors.As(err, &refusal) {
			t.Fatalf("RefuseIncompleteEnv = %v, want a missing-secret refusal", err)
		}
		if len(refusal.Problems) != 1 || refusal.Problems[0].GetKey() != "DB_PASSWORD" {
			t.Fatalf("problems = %v, want the unset secret named", refusal.Problems)
		}

		store, _ := s.env.current()
		found, err := store.Deliver(ctx, []variables.Coordinate{{Cell: variables.Cell{Key: "DB_PASSWORD"}}})
		if err != nil {
			t.Fatalf("Deliver: %v", err)
		}
		if len(found) != 0 {
			t.Errorf("Reveal = %v, want nothing: presence is not a value", found)
		}
	})

	t.Run("refuses both a plain key and a secret key when neither has a value", func(t *testing.T) {
		t.Parallel()
		s, url := serveValues(t, map[string]string{}, variables.Scope{Apps: []variables.App{{Name: "web"}}})

		declareEnv(t, url,
			&resourcesv1.VariableDefinition{Key: "DB_PASSWORD", Class: resourcesv1.VariableClass_VARIABLE_CLASS_SECRET, Required: true},
			&resourcesv1.VariableDefinition{Key: "DATABASE_URL", Class: resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN, Required: true},
		)

		err := s.RefuseIncompleteEnv(context.Background())
		var refusal *variables.MissingError
		if !errors.As(err, &refusal) {
			t.Fatalf("RefuseIncompleteEnv = %v (%T), want *variables.MissingError", err, err)
		}
		if len(refusal.Problems) != 2 || refusal.Problems[0].GetKey() != "DB_PASSWORD" || refusal.Problems[1].GetKey() != "DATABASE_URL" {
			t.Fatalf("problems = %v, want both unset keys named", refusal.Problems)
		}
	})

	t.Run("states presence for every folder a secret key is scoped to", func(t *testing.T) {
		t.Parallel()
		s, url := serveValues(t, map[string]string{"DB_PASSWORD": "local-password"}, variables.Scope{
			Apps: []variables.App{{Name: "web", Folder: "/web"}, {Name: "admin", Folder: "/admin"}},
		})

		msg := declareEnv(t, url, &resourcesv1.VariableDefinition{
			Key: "DB_PASSWORD", Class: resourcesv1.VariableClass_VARIABLE_CLASS_SECRET, Required: true,
			Folders: []string{"/web", "/admin"},
		})

		var folders []string
		for _, cell := range msg.GetCells() {
			folders = append(folders, cell.GetFolder())
		}
		slices.Sort(folders)
		if want := []string{"/admin", "/web"}; !slices.Equal(folders, want) {
			t.Fatalf("folders = %v, want one presence cell per folder in the scope", folders)
		}
		if err := s.RefuseIncompleteEnv(context.Background()); err != nil {
			t.Fatalf("RefuseIncompleteEnv = %v, want nil", err)
		}
	})
}

func TestOptionalEnvGroupWithNoValuesDoesNotBlockTheApp(t *testing.T) {
	t.Parallel()
	s := newDevServer(&fakeResources{})
	s.UseValues(map[string]string{}, variables.Scope{Apps: []variables.App{{Name: "console"}}})
	_, err := s.env.DeclareEnv(context.Background(), &resourcesv1.DeclareEnvRequest{
		Definitions: []*resourcesv1.VariableDefinition{
			{Key: "GITHUB_CLIENT_ID", Class: resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN, Required: true, Group: "githubAuth"},
			{Key: "GITHUB_CLIENT_SECRET", Class: resourcesv1.VariableClass_VARIABLE_CLASS_SECRET, Required: true, Group: "githubAuth"},
		},
		Groups: []*resourcesv1.GroupDefinition{{Key: "githubAuth"}},
	})
	if err != nil {
		t.Fatalf("DeclareEnv: %v", err)
	}
	if err := s.RefuseIncompleteEnv(context.Background()); err != nil {
		t.Fatalf("RefuseIncompleteEnv = %v, want an absent optional group to leave the app ready", err)
	}
}

func TestOptionalEnvGroupRequiresAllRequiredMembersOnceAnyValueIsSet(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		values  map[string]string
		missing []string
	}{
		{name: "only the plain member needs the secret", values: map[string]string{"GITHUB_CLIENT_ID": "id"}, missing: []string{"GITHUB_CLIENT_SECRET"}},
		{name: "only the secret needs the plain member", values: map[string]string{"GITHUB_CLIENT_SECRET": "secret"}, missing: []string{"GITHUB_CLIENT_ID"}},
		{name: "only an optional member needs both required members", values: map[string]string{"GITHUB_SCOPES": "repo"}, missing: []string{"GITHUB_CLIENT_ID", "GITHUB_CLIENT_SECRET"}},
		{name: "both required members leave the group ready", values: map[string]string{"GITHUB_CLIENT_ID": "id", "GITHUB_CLIENT_SECRET": "secret"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := newDevServer(&fakeResources{})
			s.UseValues(tc.values, variables.Scope{Apps: []variables.App{{Name: "console", Folder: "/apps/web"}}})
			response, err := s.env.DeclareEnv(context.Background(), &resourcesv1.DeclareEnvRequest{
				Definitions: []*resourcesv1.VariableDefinition{
					{Key: "GITHUB_CLIENT_ID", Class: resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN, Required: true, Group: "githubAuth"},
					{Key: "GITHUB_CLIENT_SECRET", Class: resourcesv1.VariableClass_VARIABLE_CLASS_SECRET, Required: true, Group: "githubAuth"},
					{Key: "GITHUB_SCOPES", Class: resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN, Group: "githubAuth"},
				},
				Groups: []*resourcesv1.GroupDefinition{{Key: "githubAuth"}},
			})
			if err != nil {
				t.Fatalf("DeclareEnv: %v", err)
			}
			for _, cell := range response.GetCells() {
				if cell.GetKey() == "GITHUB_CLIENT_SECRET" && cell.GetValue() != "" {
					t.Fatal("DeclareEnv revealed the secret")
				}
			}
			err = s.RefuseIncompleteEnv(context.Background())
			if len(tc.missing) == 0 {
				if err != nil {
					t.Fatalf("RefuseIncompleteEnv = %v, want the app ready", err)
				}
				return
			}
			var refusal *variables.MissingError
			if !errors.As(err, &refusal) {
				t.Fatalf("RefuseIncompleteEnv = %v, want a refusal naming %v", err, tc.missing)
			}
			var missing []string
			for _, problem := range refusal.Problems {
				missing = append(missing, problem.GetKey())
			}
			if !slices.Equal(missing, tc.missing) {
				t.Errorf("missing = %v, want %v", missing, tc.missing)
			}
		})
	}
}

func TestResetManifest(t *testing.T) {
	t.Parallel()

	t.Run("forgets the prior run's verdict", func(t *testing.T) {
		t.Parallel()
		s, url := serveValues(t, map[string]string{}, variables.Scope{})

		declareEnv(t, url, &resourcesv1.VariableDefinition{
			Key: "GONE", Class: resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN, Required: true,
		})
		if err := s.RefuseIncompleteEnv(context.Background()); err == nil {
			t.Fatal("RefuseIncompleteEnv = nil, want a refusal before the reset")
		}

		s.ResetDeclarations()

		if err := s.RefuseIncompleteEnv(context.Background()); err != nil {
			t.Fatalf("RefuseIncompleteEnv = %v, want nil — the declaration that failed is gone", err)
		}
	})

	t.Run("forgets the prior run's scopes", func(t *testing.T) {
		t.Parallel()
		s, url := serveValues(t, map[string]string{"API_BASE": "http://localhost:3000"}, variables.Scope{
			Apps: []variables.App{{Name: "web"}},
		})

		declareEnv(t, url, &resourcesv1.VariableDefinition{
			Key: "API_BASE", Class: resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN, Required: true,
			Folders: []string{"/web"},
		})

		s.ResetDeclarations()

		msg := declareEnv(t, url, &resourcesv1.VariableDefinition{
			Key: "API_BASE", Class: resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN, Required: true,
		})
		if len(msg.GetCells()) != 1 || msg.GetCells()[0].GetFolder() != "" {
			t.Fatalf("cells = %v, want one root cell: the folder scope belonged to the discovery that is gone", msg.GetCells())
		}
		if err := s.RefuseIncompleteEnv(context.Background()); err != nil {
			t.Fatalf("RefuseIncompleteEnv = %v, want nil", err)
		}
	})
}

func TestScopedFolders(t *testing.T) {
	t.Parallel()

	t.Run("lists every folder every declaration names", func(t *testing.T) {
		t.Parallel()
		s, url := serveValues(t, map[string]string{}, variables.Scope{})

		declareEnv(t, url,
			&resourcesv1.VariableDefinition{Key: "API_BASE", Folders: []string{"/web"}},
			&resourcesv1.VariableDefinition{Key: "DATABASE_URL"},
		)
		declareEnv(t, url, &resourcesv1.VariableDefinition{Key: "API_BASE", Folders: []string{"/admin", "/web"}})
		declareEnv(t, url, &resourcesv1.VariableDefinition{Key: "API_BASE", Folders: []string{"/admin"}})

		got := s.ScopedFolders()
		if len(got) != 1 {
			t.Fatalf("ScopedFolders = %v, want exactly one key: DATABASE_URL is unscoped", got)
		}
		if want := []string{"/admin", "/web"}; !slices.Equal(got["API_BASE"], want) {
			t.Fatalf("ScopedFolders[API_BASE] = %v, want %v: both declarations' folders, once each", got["API_BASE"], want)
		}
	})
}

func TestTheDevServerNamesThePublicKeysDeclaredForTheNextAppsItRuns(t *testing.T) {
	t.Parallel()

	s, url := serveValues(t, map[string]string{}, variables.Scope{})
	declareEnv(t, url,
		&resourcesv1.VariableDefinition{Key: "NEXT_PUBLIC_SHARED", Class: resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN},
		&resourcesv1.VariableDefinition{Key: "NEXT_PUBLIC_ADMIN_URL", Class: resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN, Folders: []string{"/admin"}},
	)
	web := variables.App{Name: "web", Folder: "/web", Framework: buildoutput.FrameworkNext}
	admin := variables.App{Name: "admin", Folder: "/admin", Framework: buildoutput.FrameworkNext}
	kit := variables.App{Name: "kit", Framework: buildoutput.FrameworkSvelteKit}

	for _, tc := range []struct {
		name string
		apps []variables.App
		want []string
	}{
		{"one next app, without another app's scoped key", []variables.App{web}, []string{processenv.NextPublicURLEnvVar, "NEXT_PUBLIC_SHARED"}},
		{"every next app the command runs", []variables.App{web, admin}, []string{"NEXT_PUBLIC_ADMIN_URL", processenv.NextPublicURLEnvVar, "NEXT_PUBLIC_SHARED"}},
		{"no next app", []variables.App{kit}, nil},
	} {
		if got := s.PublicKeys(tc.apps); !slices.Equal(got, tc.want) {
			t.Errorf("%s: PublicKeys = %v, want %v", tc.name, got, tc.want)
		}
	}
}
