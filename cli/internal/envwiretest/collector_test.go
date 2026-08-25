package envwiretest

import (
	"cmp"
	"context"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/deploycollector"
	"github.com/ocelhq/ocel/cli/internal/envgate"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
)

func TestDefineEnv(t *testing.T) {
	t.Run("declares through the real wire into the gate", func(t *testing.T) {
		root := setUpFixture(t, envFixture)

		values := &fakeValues{plaintext: map[envgate.Cell]string{
			{Key: "PUBLIC_SITE_URL"}:            "https://example.com",
			{Key: "PORT"}:                       "80",
			{Key: "DB_PASSWORD"}:                "hunter2",
			{Key: "POSTHOG_ID", Folder: "/web"}: "ph_web",
		}}
		gate := envgate.New(values, envgate.Scope{Apps: []envgate.App{
			{Name: "web", Folder: "/web"},
			{Name: "admin", Folder: "/admin"},
		}})

		runDiscovery(t, root, gate)

		definitions := byKey(t, gate.Definitions())
		source := filepath.Join(root, "ocel", "env.ts")

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

		t.Run("the verdict is exactly the cells the two halves owe", func(t *testing.T) {
			refusal := refuse(t, gate)
			got := describeProblems(refusal.Problems)
			want := []string{
				"PORT@ KIND_INVALID",
				"POSTHOG_ID@/admin KIND_MISSING",
				"STRIPE_API_KEY@ KIND_MISSING",
			}
			if strings.Join(got, "\n") != strings.Join(want, "\n") {
				t.Fatalf("problems =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
			}
		})

		t.Run("an invalid value reports the schema's own complaint", func(t *testing.T) {
			refusal := refuse(t, gate)
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
			if values.revealed(envgate.Cell{Key: "DB_PASSWORD"}) {
				t.Fatal("Reveal was called for the secret-class cell")
			}
			if !values.revealed(envgate.Cell{Key: "PORT"}) {
				t.Fatal("Reveal was never called for a plain cell, so the check above proves nothing")
			}
		})
	})
}

func runDiscovery(t *testing.T, root string, gate *envgate.Gate) {
	t.Helper()

	cfg := &projectconfig.Config{
		Slug:      "collector",
		Dir:       root,
		Discovery: projectconfig.Discovery{Paths: []string{"ocel"}},
	}

	var stdout, stderr strings.Builder
	if _, err := deploycollector.Collect(context.Background(), cfg, gate, &stdout, &stderr); err != nil {
		t.Fatalf("discovery: %v\nstdout: %s\nstderr: %s", err, stdout.String(), stderr.String())
	}
}

func refuse(t *testing.T, gate *envgate.Gate) *envgate.Refusal {
	t.Helper()
	err := gate.Check()
	refusal, ok := err.(*envgate.Refusal)
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

type fakeValues struct {
	plaintext map[envgate.Cell]string

	mu    sync.Mutex
	reads []envgate.Cell
}

func (v *fakeValues) List(context.Context) ([]envgate.Stored, error) {
	var stored []envgate.Stored
	for cell := range v.plaintext {
		stored = append(stored, envgate.Stored{Address: envgate.Address{Cell: cell}, Version: 1})
	}
	slices.SortFunc(stored, func(a, b envgate.Stored) int {
		if c := cmp.Compare(a.Cell.Key, b.Cell.Key); c != 0 {
			return c
		}
		return cmp.Compare(a.Cell.Folder, b.Cell.Folder)
	})
	return stored, nil
}

func (v *fakeValues) Reveal(_ context.Context, rows []envgate.Address) (map[envgate.Cell]string, error) {
	v.mu.Lock()
	for _, row := range rows {
		v.reads = append(v.reads, row.Cell)
	}
	v.mu.Unlock()

	found := map[envgate.Cell]string{}
	for _, row := range rows {
		cell := row.Cell
		if value, ok := v.plaintext[cell]; ok {
			found[cell] = value
		}
	}
	return found, nil
}

func (v *fakeValues) revealed(cell envgate.Cell) bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	for _, read := range v.reads {
		if read == cell {
			return true
		}
	}
	return false
}
