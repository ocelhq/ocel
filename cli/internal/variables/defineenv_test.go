package variables_test

import (
	"cmp"
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/cli/clitest"
	"github.com/ocelhq/ocel/cli/internal/declaration"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/variables"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
)

func TestDefineEnvDeclaresThroughDeclarationCollection(t *testing.T) {
	t.Run("declares through the real wire into the declarations", func(t *testing.T) {
		root := setUpFixture(t, envFixture)

		values := &readTrackingValues{plaintext: map[variables.Cell]string{
			{Key: "PUBLIC_SITE_URL"}:            "https://example.com",
			{Key: "PORT"}:                       "80",
			{Key: "DB_PASSWORD"}:                "hunter2",
			{Key: "POSTHOG_ID", Folder: "/web"}: "ph_web",
		}}
		declarations := variables.NewDeclarations(values, variables.Scope{Apps: []variables.App{
			{Name: "web", Folder: "/web"},
			{Name: "admin", Folder: "/admin"},
		}})

		runDiscovery(t, root, declarations)

		definitions := byKey(t, declarations.Definitions())
		source := filepath.Join(clitest.DiscoveryDir(root), "env.ts")

		t.Run("every declared key arrives", func(t *testing.T) {
			var keys []string
			for key := range definitions {
				keys = append(keys, key)
			}
			slices.Sort(keys)
			want := []string{"DB_PASSWORD", "LOG_LEVEL", "PORT", "POSTHOG_ID", "PUBLIC_SITE_URL", "STRIPE_API_KEY"}
			if strings.Join(keys, ",") != strings.Join(want, ",") {
				t.Fatalf("declared keys = %v, want %v", keys, want)
			}
		})

		t.Run("class maps onto the wire enum", func(t *testing.T) {
			for key, want := range map[string]resourcesv1.VariableClass{
				"PUBLIC_SITE_URL": resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN,
				"PORT":            resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN,
				"STRIPE_API_KEY":  resourcesv1.VariableClass_VARIABLE_CLASS_SENSITIVE,
				"DB_PASSWORD":     resourcesv1.VariableClass_VARIABLE_CLASS_SECRET,
			} {
				if got := definitions[key].GetClass(); got != want {
					t.Errorf("%s class = %v, want %v", key, got, want)
				}
			}
		})

		t.Run("required is derived from the schema", func(t *testing.T) {
			for key, want := range map[string]bool{
				"PUBLIC_SITE_URL": true,
				"PORT":            true,
				"STRIPE_API_KEY":  true,
				"DB_PASSWORD":     true,
				"LOG_LEVEL":       false,
			} {
				if got := definitions[key].GetRequired(); got != want {
					t.Errorf("%s required = %v, want %v", key, got, want)
				}
			}
		})

		t.Run("clientAccessible round-trips", func(t *testing.T) {
			if !definitions["PUBLIC_SITE_URL"].GetClientAccessible() {
				t.Error("PUBLIC_SITE_URL clientAccessible = false, want true")
			}
			for _, key := range []string{"PORT", "LOG_LEVEL", "STRIPE_API_KEY", "DB_PASSWORD", "POSTHOG_ID"} {
				if definitions[key].GetClientAccessible() {
					t.Errorf("%s clientAccessible = true, want false", key)
				}
			}
		})

		t.Run("folders round-trip", func(t *testing.T) {
			if got := definitions["POSTHOG_ID"].GetFolders(); strings.Join(got, ",") != "/web,/admin" {
				t.Errorf("POSTHOG_ID folders = %v, want [/web /admin]", got)
			}
			if got := definitions["PORT"].GetFolders(); len(got) != 0 {
				t.Errorf("PORT folders = %v, want none", got)
			}
		})

		t.Run("source names the file the user wrote", func(t *testing.T) {
			for key, definition := range definitions {
				if got := definition.GetSource(); got != source {
					t.Errorf("%s source = %q, want %q", key, got, source)
				}
			}
		})

		t.Run("the verdict is exactly the cells the two halves need, in declaration order", func(t *testing.T) {
			refusal := refuse(t, declarations)
			got := describeProblems(refusal.Problems)
			want := []string{
				"PORT@ KIND_INVALID",
				"STRIPE_API_KEY@ KIND_MISSING",
				"POSTHOG_ID@/admin KIND_MISSING",
			}
			if strings.Join(got, "\n") != strings.Join(want, "\n") {
				t.Fatalf("problems =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
			}
		})

		t.Run("an invalid value reports the schema's own complaint", func(t *testing.T) {
			refusal := refuse(t, declarations)
			for _, problem := range refusal.Problems {
				if problem.GetKind() != resourcesv1.VariableProblem_KIND_INVALID {
					continue
				}
				if problem.GetDetail() == "" {
					t.Fatalf("%s: KIND_INVALID arrived with no detail", problem.GetKey())
				}
				if !strings.Contains(problem.GetDetail(), "1000") {
					t.Errorf("detail = %q, want the schema's own message about the bound", problem.GetDetail())
				}
			}
		})

		t.Run("a secret value is never revealed to the declaring process", func(t *testing.T) {
			if values.revealed(variables.Cell{Key: "DB_PASSWORD"}) {
				t.Fatal("Reveal was called for the secret-class cell")
			}
			if !values.revealed(variables.Cell{Key: "PORT"}) {
				t.Fatal("Reveal was never called for a plain cell, so the check above proves nothing")
			}
		})
	})
}

func runDiscovery(t *testing.T, root string, declarations *variables.Declarations) {
	t.Helper()

	cfg := &project.Project{
		Slug:      "collector",
		Dir:       root,
		Discovery: project.Discovery{Paths: []string{filepath.Base(clitest.DiscoveryDir(root))}},
	}

	var stdout, stderr strings.Builder
	if _, err := declaration.Collect(context.Background(), cfg, declarations, &stdout, &stderr); err != nil {
		t.Fatalf("discovery: %v\nstdout: %s\nstderr: %s", err, stdout.String(), stderr.String())
	}
}

func refuse(t *testing.T, declarations *variables.Declarations) *variables.MissingError {
	t.Helper()
	err := declarations.RefuseIncomplete()
	refusal := &variables.MissingError{}
	ok := errors.As(err, &refusal)
	if !ok {
		t.Fatalf("Check() = %v, want a *Refusal", err)
	}
	return refusal
}

func byKey(t *testing.T, definitions []*resourcesv1.VariableDefinition) map[string]*resourcesv1.VariableDefinition {
	t.Helper()
	out := make(map[string]*resourcesv1.VariableDefinition, len(definitions))
	for _, definition := range definitions {
		if _, seen := out[definition.GetKey()]; seen {
			t.Fatalf("%s was declared twice", definition.GetKey())
		}
		out[definition.GetKey()] = definition
	}
	return out
}

type readTrackingValues struct {
	plaintext map[variables.Cell]string

	mu    sync.Mutex
	reads []variables.Cell
}

func (v *readTrackingValues) List(context.Context) ([]variables.ValueMetadata, error) {
	var stored []variables.ValueMetadata
	for cell := range v.plaintext {
		stored = append(stored, variables.ValueMetadata{Coordinate: variables.Coordinate{Cell: cell}, Version: 1})
	}
	slices.SortFunc(stored, func(a, b variables.ValueMetadata) int {
		if c := cmp.Compare(a.Cell.Key, b.Cell.Key); c != 0 {
			return c
		}
		return cmp.Compare(a.Cell.Folder, b.Cell.Folder)
	})
	return stored, nil
}

func (v *readTrackingValues) Reveal(_ context.Context, rows []variables.Coordinate) (map[variables.Coordinate]string, error) {
	v.mu.Lock()
	for _, row := range rows {
		v.reads = append(v.reads, row.Cell)
	}
	v.mu.Unlock()

	found := map[variables.Coordinate]string{}
	for _, row := range rows {
		if value, ok := v.plaintext[row.Cell]; ok {
			found[row] = value
		}
	}
	return found, nil
}

func (v *readTrackingValues) revealed(cell variables.Cell) bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	for _, read := range v.reads {
		if read == cell {
			return true
		}
	}
	return false
}
