// Package envwire holds one test and nothing else: the real ocel/env SDK,
// running in a real node process started by the real discovery path, talking
// to the real generated Connect handler in front of a real envgate.Gate.
//
// Every other test of this feature stands one half of that exchange in for the
// other — the SDK's suite mocks its transport, and the CLI's suite drives a
// hand-written TypeScript stand-in that speaks Connect JSON the SDK never
// emits. So a field that stopped arriving, a class that mapped to the wrong
// enum, or a source path that degraded to a bundle path would leave every
// suite green. This package exists to make exactly those failures loud, and it
// asserts nothing that would still pass with the transport faked.
//
// It lives in its own package so CI can name it in a `go test` argument
// without pulling in the rest of the CLI's suites.
package envwire

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/deploycollector"
	"github.com/ocelhq/ocel/cli/internal/envgate"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/resources/v1"
)

// envFixture is the declaration under test, written in the language a user
// writes it in and importing the published `ocel/env` subpath so the package's
// own export map is part of what is exercised.
//
// The schemas are real zod schemas rather than hand-rolled stand-ins because
// the detail text a rejected value travels with is produced by the SDK from
// whatever the schema said, and a stand-in would let that text be anything.
const envFixture = `
import { defineEnv } from "ocel/env";
import { z } from "zod";

export const env = defineEnv({
  PUBLIC_SITE_URL: { class: "plain", client: true },
  PORT: { class: "plain", schema: z.coerce.number().min(1000) },
  LOG_LEVEL: { class: "plain", schema: z.string().default("info") },
  STRIPE_API_KEY: { class: "sensitive" },
  DB_PASSWORD: { class: "secret" },
  POSTHOG_ID: { class: "plain", folders: ["/web", "/admin"] },
});
`

func TestDefineEnv_DeclaresThroughTheRealWireIntoTheGate(t *testing.T) {
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
		sort.Strings(keys)
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

	// A schema carrying a default is the only way `required` is ever false, and
	// it is derived in the SDK from the schema object itself — a live thing that
	// could not have travelled.
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

	// clientAccessible has no consumer in the CLI yet — the ticket that grows
	// one turns a wrong value here into a confidential value inside a browser
	// bundle, and this is the assertion that would have caught it. proto3 omits
	// a false, so the negative case asserts the field reads false rather than
	// that a false was transmitted.
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

	// source is walked out of a stack trace inside the SDK, mapped back through
	// the inline sourcemap esbuild wrote, by a node started with
	// --enable-source-maps. Drop any link in that chain and every diagnostic
	// that names a declaration silently starts naming a file under .ocel/.
	t.Run("source names the file the user wrote", func(t *testing.T) {
		for key, definition := range definitions {
			if got := definition.GetSource(); got != source {
				t.Errorf("%s source = %q, want %q", key, got, source)
			}
		}
	})

	// The gate's verdict is the SDK's problems merged with its own. Asserting
	// the exact set is what lets the required-cell matrix have one owner: a
	// divergence between the SDK's rule and the gate's shows up here as a
	// duplicated or misplaced cell.
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

	// KIND_INVALID can only have come from the declaring process: the gate
	// cannot run a schema. Non-empty detail is the SDK's complaint() surviving
	// the trip back.
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

	// A secret-class cell is answered with presence and nothing else, so a
	// build host never holds one. Reveal is where a plaintext would have been
	// fetched, and it must never be reached for that cell.
	t.Run("a secret value is never revealed to the declaring process", func(t *testing.T) {
		if values.revealed(envgate.Cell{Key: "DB_PASSWORD"}) {
			t.Fatal("Reveal was called for the secret-class cell")
		}
		if !values.revealed(envgate.Cell{Key: "PORT"}) {
			t.Fatal("Reveal was never called for a plain cell, so the check above proves nothing")
		}
	})
}

// runDiscovery drives the production discovery path — Discover, Bundle, and
// the node spawn with its flags — rather than re-implementing it, so the
// bundler options and the node flags are themselves under test.
func runDiscovery(t *testing.T, root string, gate *envgate.Gate) {
	t.Helper()

	cfg := &projectconfig.Config{
		Slug:      "envwire",
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

func describeProblems(problems []*resourcesv1.VariableProblem) []string {
	out := make([]string, 0, len(problems))
	for _, problem := range problems {
		out = append(out, problem.GetKey()+"@"+problem.GetFolder()+" "+problem.GetKind().String())
	}
	return out
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

// setUpFixture writes a project whose only declaration file is fixture, and
// links in just the two packages it imports. Linking the packages rather than
// installing them is what keeps this test as fast as a unit test; resolving
// `ocel` to the workspace package is what puts the published export map in the
// path.
func setUpFixture(t *testing.T, fixture string) string {
	t.Helper()
	requireNode(t)
	repo := repoRoot(t)
	requireSDKBuild(t, repo)

	root := t.TempDir()
	writeFile(t, filepath.Join(root, "ocel", "env.ts"), fixture)

	modules := filepath.Join(root, "node_modules")
	if err := os.MkdirAll(modules, 0o755); err != nil {
		t.Fatal(err)
	}
	link(t, filepath.Join(repo, "packages", "ocel"), filepath.Join(modules, "ocel"))
	link(t, filepath.Join(repo, "packages", "ocel", "node_modules", "zod"), filepath.Join(modules, "zod"))

	return root
}

// requireNode fails rather than skips. This is the repo's only test that runs
// the real SDK in a real node process; a missing node silently turns it into a
// no-op that still prints ok, which is exactly the failure this package exists
// to make loud instead.
func requireNode(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("node"); err != nil {
		t.Fatalf("node is not on PATH, so there is no wire to test.\nInstall node (this repo targets node 22) and put it on PATH: %v", err)
	}
}

// requireSDKBuild fails rather than skips. packages/ocel/dist is gitignored, so
// an unbuilt tree is the normal state of a fresh clone — and a test that
// quietly skips itself in the normal state is a test nobody notices died.
func requireSDKBuild(t *testing.T, repo string) {
	t.Helper()
	entry := filepath.Join(repo, "packages", "ocel", "dist", "env", "index.js")
	if _, err := os.Stat(entry); err != nil {
		t.Fatalf("the ocel SDK is not built (%s is missing), so there is no wire to test.\nBuild it with: pnpm --filter ocel build", entry)
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate this test file")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", ".."))
}

func link(t *testing.T, target, name string) {
	t.Helper()
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("fixture dependency %s is missing: %v", target, err)
	}
	if err := os.Symlink(target, name); err != nil {
		t.Fatal(err)
	}
}

func writeFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

// fakeValues is the store, reduced to the two questions the gate asks it. The
// store itself is out of scope here: this test is about what crosses the
// language boundary, and a real store would only add ways for it to fail.
type fakeValues struct {
	plaintext map[envgate.Cell]string

	mu    sync.Mutex
	reads []envgate.Cell
}

func (v *fakeValues) List(context.Context) ([]envgate.Stored, error) {
	var stored []envgate.Stored
	for cell := range v.plaintext {
		stored = append(stored, envgate.Stored{Cell: cell, Version: 1})
	}
	sort.Slice(stored, func(i, j int) bool {
		if stored[i].Cell.Key != stored[j].Cell.Key {
			return stored[i].Cell.Key < stored[j].Cell.Key
		}
		return stored[i].Cell.Folder < stored[j].Cell.Folder
	})
	return stored, nil
}

func (v *fakeValues) Reveal(_ context.Context, cell envgate.Cell) (string, bool, error) {
	v.mu.Lock()
	v.reads = append(v.reads, cell)
	v.mu.Unlock()
	value, ok := v.plaintext[cell]
	return value, ok, nil
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
