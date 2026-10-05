package env

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/clitest"
	"github.com/ocelhq/ocel/cli/internal/commands/bootstrap"
	"github.com/ocelhq/ocel/cli/internal/prerequisite"
	"github.com/ocelhq/ocel/cli/internal/terminal"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/processenv"
	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	variablestorev1 "github.com/ocelhq/ocel/pkg/proto/provider/variablestore/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/variablestore/v1/variablestorev1connect"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	"github.com/ocelhq/ocel/pkg/variablestore"
)

func setUpEnvFixture(t *testing.T) clitest.FakeProject {
	t.Helper()
	return setUpDeclaringProject(t, envDeclaringScript(fixtureDefinitions))
}

func setUpDeclaringProject(t *testing.T, script string) clitest.FakeProject {
	t.Helper()
	project := clitest.SetUpProject(t)
	clitest.WriteFile(t, filepath.Join(clitest.DiscoveryDir(project.Root), "env.ts"), script)
	return project
}

const fixtureDefinitions = `[
  {"key":"STRIPE_API_KEY","class":"VARIABLE_CLASS_SECRET","required":true},
  {"key":"API_TOKEN","class":"VARIABLE_CLASS_SECRET","required":true},
  {"key":"LOG_LEVEL","class":"VARIABLE_CLASS_PLAIN","required":true},
  {"key":"POSTHOG_ID","class":"VARIABLE_CLASS_PLAIN","required":true}
]`

func newStreamedDependencies(stream io.Writer) Dependencies {
	dependencies := newTestDependencies()
	clitest.AttachTerminalSink(dependencies.Invocation, stream)
	return dependencies
}

func newJSONDependencies(stream io.Writer) Dependencies {
	dependencies := newTestDependencies()
	dependencies.Presentation = clitest.ResolveJSONPresentation
	clitest.AttachTerminalSink(dependencies.Invocation, stream)
	return dependencies
}

func envSet(t *testing.T, root, key, value string, opts envOptions) string {
	t.Helper()
	var stdout, stderr bytes.Buffer
	if err := runEnvSet(context.Background(), newStreamedDependencies(&stderr), root, key, value, opts, nil, &stdout, &stderr); err != nil {
		t.Fatalf("runEnvSet(%s) err = %v; stdout=%s stderr=%s", key, err, stdout.String(), stderr.String())
	}
	return stdout.String()
}

func valuesOf(project clitest.FakeProject) variablestore.Store {
	return variablestore.Store{KeyValues: project.Provider.KeyValues(), Cipher: project.Provider.Cipher()}
}

func cellAt(key string) variablestore.Coordinate {
	return variablestore.Coordinate{Cell: variablestore.Cell{Key: key}}
}

func seedValue(t *testing.T, project clitest.FakeProject, tier environment.Tier, slug string, at variablestore.Coordinate, value string) {
	t.Helper()
	if _, err := valuesOf(project).Set(context.Background(), variablestore.Scope{Project: slug, Tier: tier}, at, value, nil); err != nil {
		t.Fatalf("seed %s in %s's %s values: %v", at, slug, tier, err)
	}
}

func seedEnvironment(t *testing.T, project clitest.FakeProject, name string) {
	t.Helper()
	if err := stackrecords.Write(context.Background(), project.Provider.KeyValues(), environment.TierPreview, clitest.FixtureSlug, naming.InfraStack(name), stackrecords.Stack{}); err != nil {
		t.Fatalf("seed the %s preview environment: %v", name, err)
	}
}

func removeEnvironment(t *testing.T, project clitest.FakeProject, name string) {
	t.Helper()
	if err := stackrecords.Forget(context.Background(), project.Provider.KeyValues(), environment.TierPreview, clitest.FixtureSlug, naming.InfraStack(name)); err != nil {
		t.Fatalf("remove the %s preview environment: %v", name, err)
	}
}

func bootstrapOnlyPreview(t *testing.T, project clitest.FakeProject) {
	t.Helper()
	clitest.Bootstrap(t, project.Provider, environment.TierPreview)
	if err := project.Provider.FakeBootstrap().Remove(context.Background(), environment.TierProduction, nil); err != nil {
		t.Fatalf("remove the production bootstrap: %v", err)
	}
}

func moveBeforeTheNextWrite(project clitest.FakeProject, key string) {
	scope := variablestore.Scope{Project: clitest.FixtureSlug, Tier: environment.TierProduction}
	project.Provider.KeyValues().(*fake.KeyValues).MoveBeforeNextWrite(variablestore.CellKey(scope, cellAt(key)))
}

func envDeclaringScript(definitions string) string {
	return envDeclaringRequest(`{"definitions": ` + definitions + `}`)
}

func envDeclaringRequest(body string) string {
	return fmt.Sprintf(`
declare global {
  var __ocelRegister: Promise<unknown>[];
}
globalThis.__ocelRegister ??= [];

globalThis.__ocelRegister.push(
  (async () => {
    const log = process.env.OCEL_TEST_DISCOVERY_LOG;
    if (log) await (await import("node:fs/promises")).appendFile(log, "ran\n");

    const res = await fetch(new URL("/app.resources.v1.ResourceService/DeclareEnv", process.env.%s), {
      method: "POST",
      headers: { "Content-Type": "application/json", Authorization: "Bearer " + process.env.`+processenv.DevServerTokenEnvVar+` },
      body: JSON.stringify(%s),
    });
    if (!res.ok) throw new Error("DeclareEnv failed: " + res.status + " " + (await res.text()));
  })(),
);
export {};
`, processenv.DevServerEnvVar, body)
}

func setUpDeclaringFixture(t *testing.T, definitions string) (root, log string) {
	t.Helper()
	root = setUpDeclaringProject(t, envDeclaringScript(definitions)).Root
	log = filepath.Join(t.TempDir(), "discovery.log")
	t.Setenv("OCEL_TEST_DISCOVERY_LOG", log)
	return root, log
}

func discoveryRuns(t *testing.T, log string) int {
	t.Helper()
	data, err := os.ReadFile(log)
	if err != nil {
		return 0
	}
	return strings.Count(string(data), "ran\n")
}

func TestSettingAValueStoresItOnlyWhereADeclarationReadsIt(t *testing.T) {
	t.Run("refuses a key no app declares", func(t *testing.T) {
		root := setUpEnvFixture(t).Root

		var stdout, stderr bytes.Buffer
		err := runEnvSet(context.Background(), newStreamedDependencies(&stderr), root, "SITE_HOSTNAME", "acme.example", envOptions{}, nil, &stdout, &stderr)
		if err == nil {
			t.Fatal("runEnvSet(SITE_HOSTNAME) err = nil, want a key nothing declares refused: the declarations deliver declared keys only, so the value would sit in the store and reach no build and no function")
		}
		for _, want := range []string{"SITE_HOSTNAME", "defineEnv"} {
			if !strings.Contains(stderr.String(), want) {
				t.Errorf("stream = %q, want %q named", stderr.String(), want)
			}
		}
	})

	t.Run("names the key it set, and the value it wrote reads back only when revealing is explicit", func(t *testing.T) {
		root := setUpEnvFixture(t).Root

		if out := envSet(t, root, "STRIPE_API_KEY", "sk_live_secret", envOptions{}); !strings.Contains(out, "STRIPE_API_KEY") {
			t.Errorf("set stdout = %q, want it to name the key it set", out)
		}

		t.Run("the value is withheld without --reveal", func(t *testing.T) {
			var plain bytes.Buffer
			if err := runEnvGet(context.Background(), newStreamedDependencies(&plain), root, "STRIPE_API_KEY", envOptions{}, &plain, &plain); err != nil {
				t.Fatalf("runEnvGet err = %v; out=%s", err, plain.String())
			}
			if strings.Contains(plain.String(), "sk_live_secret") {
				t.Errorf("get stdout = %q, want the value withheld without --reveal", plain.String())
			}
			if !strings.Contains(plain.String(), "--reveal") {
				t.Errorf("get stdout = %q, want it to name the flag that reveals", plain.String())
			}
		})

		t.Run("--reveal prints exactly the value so it is scriptable", func(t *testing.T) {
			var revealed bytes.Buffer
			var chatter bytes.Buffer
			if err := runEnvGet(context.Background(), newStreamedDependencies(&chatter), root, "STRIPE_API_KEY", envOptions{reveal: true, yes: true}, &revealed, &chatter); err != nil {
				t.Fatalf("runEnvGet --reveal err = %v; out=%s", err, revealed.String())
			}
			if strings.TrimSpace(revealed.String()) != "sk_live_secret" {
				t.Errorf("get --reveal stdout = %q, want exactly the value so it is scriptable", revealed.String())
			}
		})
	})

	t.Run("an override is its own cell beside the value bound to all environments", func(t *testing.T) {
		project := setUpEnvFixture(t)
		root := project.Root
		clitest.Bootstrap(t, project.Provider, environment.TierPreview)
		seedEnvironment(t, project, "staging")

		preview := envOptions{preview: true}
		staging := envOptions{preview: true, environment: "staging"}
		envSet(t, root, "STRIPE_API_KEY", "sk_shared", preview)
		envSet(t, root, "STRIPE_API_KEY", "sk_staging", staging)

		for name, tc := range map[string]struct {
			opts envOptions
			want string
		}{
			"the environment with the override": {opts: staging, want: "sk_staging"},
			"every other environment":           {opts: preview, want: "sk_shared"},
		} {
			t.Run(name, func(t *testing.T) {
				opts := tc.opts
				opts.reveal, opts.yes = true, true
				var stdout bytes.Buffer
				var chatter bytes.Buffer
				if err := runEnvGet(context.Background(), newStreamedDependencies(&chatter), root, "STRIPE_API_KEY", opts, &stdout, &chatter); err != nil {
					t.Fatalf("runEnvGet err = %v; out=%s", err, stdout.String())
				}
				if got := strings.TrimSpace(stdout.String()); got != tc.want {
					t.Errorf("value = %q, want %q", got, tc.want)
				}
			})
		}
	})

	t.Run("refuses an environment that does not exist, and the refused write does not land", func(t *testing.T) {
		project := setUpEnvFixture(t)
		root := project.Root
		clitest.Bootstrap(t, project.Provider, environment.TierPreview)
		seedEnvironment(t, project, "staging")

		var stdout, stderr bytes.Buffer
		err := runEnvSet(context.Background(), newStreamedDependencies(&stderr), root, "STRIPE_API_KEY", "sk_typo", envOptions{preview: true, environment: "stagng"}, nil, &stdout, &stderr)
		if err == nil {
			t.Fatal("runEnvSet against an environment that does not exist err = nil, want a refusal")
		}
		if !strings.Contains(stderr.String(), "stagng") || !strings.Contains(stderr.String(), "staging") {
			t.Errorf("stream = %q, want it to name what was asked for and what exists", stderr.String())
		}

		var get bytes.Buffer
		if err := runEnvGet(context.Background(), newStreamedDependencies(&get), root, "STRIPE_API_KEY", envOptions{preview: true, environment: "stagng", reveal: true, yes: true}, &get, &get); err == nil {
			t.Errorf("the refused write landed anyway: get = %q", get.String())
		}
	})

	t.Run("refuses an environment on production", func(t *testing.T) {
		root := setUpEnvFixture(t).Root

		var stdout, stderr bytes.Buffer
		err := runEnvSet(context.Background(), newStreamedDependencies(&stderr), root, "STRIPE_API_KEY", "sk_live", envOptions{environment: "staging"}, nil, &stdout, &stderr)
		if err == nil {
			t.Fatal("runEnvSet --environment against production err = nil, want a refusal")
		}
		if !strings.Contains(err.Error(), "--preview") {
			t.Errorf("err = %v, want it to name the flag that selects the bootstrap overrides live on", err)
		}
	})

	t.Run("refuses on preview infrastructure", func(t *testing.T) {
		project := setUpEnvFixture(t)
		root := project.Root
		bootstrapOnlyPreview(t, project)

		var stdout, stderr bytes.Buffer
		err := runEnvSet(context.Background(), newStreamedDependencies(&stderr), root, "STRIPE_API_KEY", "sk_live_secret", envOptions{}, nil, &stdout, &stderr)
		if err == nil {
			t.Fatal("runEnvSet against preview infrastructure err = nil, want a tier-mismatch refusal")
		}
	})

	t.Run("refuses a root value for a scoped key", func(t *testing.T) {
		root := setUpDeclaringProject(t, envDeclaringScript(`[{"key":"POSTHOG_ID","class":"VARIABLE_CLASS_PLAIN","required":true,"folders":["/web","/admin"]}]`)).Root

		var stdout, stderr bytes.Buffer
		err := runEnvSet(context.Background(), newStreamedDependencies(&stderr), root, "POSTHOG_ID", "ph_root", envOptions{}, nil, &stdout, &stderr)
		if err == nil {
			t.Fatal("runEnvSet err = nil, want a root value for a scoped key refused: nothing could ever read it")
		}
		for _, want := range []string{"POSTHOG_ID", "/web", "/admin"} {
			if !strings.Contains(stderr.String(), want) {
				t.Errorf("stream = %q, want it to name %q", stderr.String(), want)
			}
		}
	})

	t.Run("refuses a scoped key in a folder it does not name", func(t *testing.T) {
		root := setUpDeclaringProject(t, envDeclaringScript(`[{"key":"POSTHOG_ID","class":"VARIABLE_CLASS_PLAIN","required":true,"folders":["/web"]}]`)).Root

		var stdout, stderr bytes.Buffer
		err := runEnvSet(context.Background(), newStreamedDependencies(&stderr), root, "POSTHOG_ID", "ph", envOptions{folder: "/admin"}, nil, &stdout, &stderr)
		if err == nil {
			t.Fatal("runEnvSet err = nil, want a folder outside the key's scope refused")
		}
		if !strings.Contains(stderr.String(), "/admin") {
			t.Errorf("stream = %q, want it to name the folder it refused", stderr.String())
		}
	})

	t.Run("accepts a scoped key in a folder it names", func(t *testing.T) {
		root := setUpDeclaringProject(t, envDeclaringScript(`[{"key":"POSTHOG_ID","class":"VARIABLE_CLASS_PLAIN","required":true,"folders":["/web"]}]`)).Root

		if out := envSet(t, root, "POSTHOG_ID", "ph_web", envOptions{folder: "/web"}); !strings.Contains(out, "/web") {
			t.Errorf("set stdout = %q, want the folder it wrote named", out)
		}
	})

	t.Run("leaves an unscoped key writable at root and in a folder", func(t *testing.T) {
		root := setUpDeclaringProject(t, envDeclaringScript(`[{"key":"LOG_LEVEL","class":"VARIABLE_CLASS_PLAIN","required":true}]`)).Root

		envSet(t, root, "LOG_LEVEL", "info", envOptions{})
		envSet(t, root, "LOG_LEVEL", "debug", envOptions{folder: "/web"})
	})

	t.Run("a second write reuses the declarations the first one learned", func(t *testing.T) {
		root, log := setUpDeclaringFixture(t, `[{"key":"POSTHOG_ID","class":"VARIABLE_CLASS_PLAIN","required":true,"folders":["/web"]}]`)

		envSet(t, root, "POSTHOG_ID", "ph_one", envOptions{folder: "/web"})
		envSet(t, root, "POSTHOG_ID", "ph_two", envOptions{folder: "/web"})

		if got := discoveryRuns(t, log); got != 1 {
			t.Errorf("discovery ran %d times over two writes, want 1: nothing the declarations come from changed between them", got)
		}

		var out bytes.Buffer
		var chatter bytes.Buffer
		if err := runEnvGet(context.Background(), newStreamedDependencies(&chatter), root, "POSTHOG_ID", envOptions{folder: "/web", reveal: true}, &out, &chatter); err != nil {
			t.Fatalf("runEnvGet err = %v; out=%s", err, out.String())
		}
		if strings.TrimSpace(out.String()) != "ph_two" {
			t.Errorf("value in /web = %q, want %q: the second write must land like the first", out.String(), "ph_two")
		}
	})

	t.Run("picks up a scope the code gained since the last write", func(t *testing.T) {
		root, log := setUpDeclaringFixture(t, `[{"key":"POSTHOG_ID","class":"VARIABLE_CLASS_PLAIN","required":true}]`)

		envSet(t, root, "POSTHOG_ID", "ph_root", envOptions{})

		clitest.WriteFile(t, filepath.Join(clitest.DiscoveryDir(root), "env.ts"),
			envDeclaringScript(`[{"key":"POSTHOG_ID","class":"VARIABLE_CLASS_PLAIN","required":true,"folders":["/web"]}]`))

		var stdout, stderr bytes.Buffer
		err := runEnvSet(context.Background(), newStreamedDependencies(&stderr), root, "POSTHOG_ID", "ph_root_again", envOptions{}, nil, &stdout, &stderr)
		if err == nil {
			t.Fatal("runEnvSet err = nil, want the scope the code now declares to refuse a root write")
		}
		if !strings.Contains(stderr.String(), "/web") {
			t.Errorf("stream = %q, want it to name the folder the key is now scoped to", stderr.String())
		}
		if got := discoveryRuns(t, log); got != 2 {
			t.Errorf("discovery ran %d times, want 2: the declaring code changed between the writes", got)
		}
	})

	t.Run("does not trust a cached absence for a conditionally scoped key", func(t *testing.T) {
		t.Setenv("OCEL_TEST_ENV_DEFINITIONS", `[{"key":"LOG_LEVEL","class":"VARIABLE_CLASS_PLAIN","required":true}]`)
		root := setUpDeclaringProject(t, clitest.EnvDeclareOnlyScript).Root

		envSet(t, root, "LOG_LEVEL", "info", envOptions{})

		t.Setenv("OCEL_TEST_ENV_DEFINITIONS", `[{"key":"POSTHOG_ID","class":"VARIABLE_CLASS_PLAIN","required":true,"folders":["/web"]}]`)

		var stdout, stderr bytes.Buffer
		err := runEnvSet(context.Background(), newStreamedDependencies(&stderr), root, "POSTHOG_ID", "ph_root", envOptions{}, nil, &stdout, &stderr)
		if err == nil {
			t.Fatal("runEnvSet err = nil, want a root value for a scoped key refused: a cached set that never mentioned the key cannot say it is unscoped")
		}
		if !strings.Contains(stderr.String(), "/web") {
			t.Errorf("stream = %q, want it to name the folder the key is scoped to", stderr.String())
		}

		var out bytes.Buffer
		if err := runEnvGet(context.Background(), newStreamedDependencies(&out), root, "POSTHOG_ID", envOptions{reveal: true}, &out, &out); err == nil {
			t.Errorf("runEnvGet at root err = nil (out=%q), want no root cell written", out.String())
		}
	})
}

func TestEnvAsksTheBootstrapOnlyWhetherThisCLICanSpeakToIt(t *testing.T) {
	root := clitest.SetUpProject(t).Root
	dependencies := newTestDependencies()

	var stdout, stderr bytes.Buffer
	if err := runEnvList(context.Background(), dependencies, root, envOptions{}, &stdout, &stderr); err != nil {
		t.Fatalf("a variable this bootstrap stores was refused over a feature no variable needs: %v", err)
	}
}

func TestGettingAValueReadsOneCellOfOneTier(t *testing.T) {
	t.Run("reports an unset key", func(t *testing.T) {
		root := setUpEnvFixture(t).Root

		var stdout, stderr bytes.Buffer
		err := runEnvGet(context.Background(), newStreamedDependencies(&stderr), root, "NEVER_SET", envOptions{reveal: true}, &stdout, &stderr)
		if err == nil {
			t.Fatal("runEnvGet on an unset key err = nil, want a failure rather than an empty value")
		}
		if !strings.Contains(stderr.String(), "NEVER_SET") {
			t.Errorf("stream = %q, want it to name the key", stderr.String())
		}
	})

	t.Run("a folder and the root are separate cells", func(t *testing.T) {
		root := setUpEnvFixture(t).Root
		envSet(t, root, "POSTHOG_ID", "web-id", envOptions{folder: "/web"})

		var stdout, stderr bytes.Buffer
		if err := runEnvGet(context.Background(), newStreamedDependencies(&stderr), root, "POSTHOG_ID", envOptions{reveal: true}, &stdout, &stderr); err == nil {
			t.Fatalf("runEnvGet at root err = nil (out=%q), want the root cell to be unset", stdout.String())
		}

		var folder bytes.Buffer
		var chatter bytes.Buffer
		if err := runEnvGet(context.Background(), newStreamedDependencies(&chatter), root, "POSTHOG_ID", envOptions{folder: "/web", reveal: true}, &folder, &chatter); err != nil {
			t.Fatalf("runEnvGet in /web err = %v", err)
		}
		if strings.TrimSpace(folder.String()) != "web-id" {
			t.Errorf("get in /web = %q, want %q", folder.String(), "web-id")
		}
	})

	t.Run("production and preview are separate stores", func(t *testing.T) {
		project := setUpEnvFixture(t)
		root := project.Root
		clitest.Bootstrap(t, project.Provider, environment.TierPreview)
		envSet(t, root, "STRIPE_API_KEY", "sk_live_secret", envOptions{})

		var get bytes.Buffer
		if err := runEnvGet(context.Background(), newStreamedDependencies(&get), root, "STRIPE_API_KEY", envOptions{preview: true, reveal: true, yes: true}, &get, &get); err == nil {
			t.Errorf("preview get err = nil (out=%q), want the production value unreadable from preview", get.String())
		}

		var ls bytes.Buffer
		if err := runEnvList(context.Background(), newStreamedDependencies(&ls), root, envOptions{preview: true}, &ls, &ls); err != nil {
			t.Fatalf("runEnvList --preview err = %v; out=%s", err, ls.String())
		}
		if strings.Contains(ls.String(), "STRIPE_API_KEY") {
			t.Errorf("preview ls = %q, want no production value listed", ls.String())
		}

		envSet(t, root, "STRIPE_API_KEY", "sk_test_preview", envOptions{preview: true})

		var production bytes.Buffer
		var chatter bytes.Buffer
		if err := runEnvGet(context.Background(), newStreamedDependencies(&chatter), root, "STRIPE_API_KEY", envOptions{reveal: true, yes: true}, &production, &chatter); err != nil {
			t.Fatalf("runEnvGet err = %v; out=%s", err, production.String())
		}
		if got := strings.TrimSpace(production.String()); got != "sk_live_secret" {
			t.Errorf("production value = %q, want %q: a preview write must not reach production", got, "sk_live_secret")
		}
	})
}

func TestRemovingAValueDeletesItsCell(t *testing.T) {
	t.Run("removes the value", func(t *testing.T) {
		root := setUpEnvFixture(t).Root
		envSet(t, root, "STRIPE_API_KEY", "sk_live_secret", envOptions{})

		var stdout, stderr bytes.Buffer
		if err := runEnvRemove(context.Background(), newStreamedDependencies(&stderr), root, "STRIPE_API_KEY", envOptions{}, &stdout, &stderr); err != nil {
			t.Fatalf("runEnvRemove err = %v; stderr=%s", err, stderr.String())
		}
		if !strings.Contains(stdout.String(), "STRIPE_API_KEY") {
			t.Errorf("rm stdout = %q, want it to name the removed key", stdout.String())
		}

		var after bytes.Buffer
		if err := runEnvList(context.Background(), newStreamedDependencies(&after), root, envOptions{}, &after, &after); err != nil {
			t.Fatalf("runEnvList err = %v", err)
		}
		if strings.Contains(after.String(), "STRIPE_API_KEY") {
			t.Errorf("ls after rm = %q, want the value gone", after.String())
		}
	})

	t.Run("reports nothing to remove", func(t *testing.T) {
		root := setUpEnvFixture(t).Root

		var stdout, stderr bytes.Buffer
		if err := runEnvRemove(context.Background(), newStreamedDependencies(&stderr), root, "NEVER_SET", envOptions{}, &stdout, &stderr); err != nil {
			t.Fatalf("runEnvRemove err = %v; stderr=%s", err, stderr.String())
		}
		if !strings.Contains(stdout.String(), "No value") {
			t.Errorf("rm of an unset key = %q, want it to say there was nothing set", stdout.String())
		}
	})

	t.Run("an orphaned override is listed and removable", func(t *testing.T) {
		project := setUpEnvFixture(t)
		root := project.Root
		clitest.Bootstrap(t, project.Provider, environment.TierPreview)
		seedEnvironment(t, project, "staging")
		envSet(t, root, "STRIPE_API_KEY", "sk_staging", envOptions{preview: true, environment: "staging"})

		removeEnvironment(t, project, "staging")

		var ls bytes.Buffer
		if err := runEnvList(context.Background(), newStreamedDependencies(&ls), root, envOptions{preview: true}, &ls, &ls); err != nil {
			t.Fatalf("runEnvList err = %v; out=%s", err, ls.String())
		}
		if !strings.Contains(ls.String(), "orphaned") {
			t.Errorf("ls = %q, want the override marked orphaned once its environment is gone", ls.String())
		}

		var rm bytes.Buffer
		if err := runEnvRemove(context.Background(), newStreamedDependencies(&rm), root, "STRIPE_API_KEY", envOptions{preview: true, environment: "staging"}, &rm, &rm); err != nil {
			t.Fatalf("runEnvRemove err = %v; out=%s", err, rm.String())
		}
		if !strings.Contains(rm.String(), "Removed") {
			t.Errorf("rm = %q, want the orphan removed rather than reported unset", rm.String())
		}

		var after bytes.Buffer
		if err := runEnvList(context.Background(), newStreamedDependencies(&after), root, envOptions{preview: true}, &after, &after); err != nil {
			t.Fatalf("runEnvList err = %v; out=%s", err, after.String())
		}
		if strings.Contains(after.String(), "STRIPE_API_KEY") {
			t.Errorf("ls = %q, want the removed orphan gone from the listing", after.String())
		}
	})
}

func TestAValuesHistoryShowsMetadataNewestFirst(t *testing.T) {
	t.Run("shows metadata newest first and never a plaintext", func(t *testing.T) {
		root := setUpEnvFixture(t).Root
		secrets := []string{"sk_first", "sk_second", "sk_third"}
		for _, v := range secrets {
			envSet(t, root, "STRIPE_API_KEY", v, envOptions{})
		}

		for name, opts := range map[string]envOptions{
			"without --reveal": {},
			"with --reveal":    {reveal: true},
		} {
			t.Run(name, func(t *testing.T) {
				var stdout bytes.Buffer
				var chatter bytes.Buffer
				if err := runEnvHistory(context.Background(), newStreamedDependencies(&chatter), root, "STRIPE_API_KEY", opts, &stdout, &chatter); err != nil {
					t.Fatalf("runEnvHistory(reveal=%v) err = %v; out=%s", opts.reveal, err, stdout.String())
				}
				out := stdout.String()

				for _, secret := range secrets {
					if strings.Contains(out, secret) {
						t.Errorf("history(reveal=%v) stdout = %q, want no plaintext (found %q)", opts.reveal, out, secret)
					}
				}
				if strings.Contains(out, "VALUE") {
					t.Errorf("history(reveal=%v) stdout = %q, want no VALUE column", opts.reveal, out)
				}

				rows := strings.Split(strings.TrimSpace(out), "\n")
				if len(rows) != 4 {
					t.Fatalf("history(reveal=%v) stdout = %q, want a header and three versions", opts.reveal, out)
				}
				for i, wantVersion := range []string{"3", "2", "1"} {
					if got := strings.Fields(rows[i+1])[0]; got != wantVersion {
						t.Errorf("history(reveal=%v) row %d = %q, want version %s: newest first", opts.reveal, i, rows[i+1], wantVersion)
					}
				}
			})
		}
	})
}

func TestSettingSeveralPairsValidatesEveryPairBeforeWritingAny(t *testing.T) {
	t.Run("sets every declared pair", func(t *testing.T) {
		root := setUpEnvFixture(t).Root
		var stdout, stderr bytes.Buffer
		err := runEnvSetPairs(context.Background(), newTestDependencies(), root, []envSetPair{
			{key: "STRIPE_API_KEY", value: "sk_live_secret"},
			{key: "LOG_LEVEL", value: "debug"},
		}, envOptions{}, nil, &stdout, &stderr)
		if err != nil {
			t.Fatalf("runEnvSetPairs() = %v; stdout=%s", err, stdout.String())
		}
		for _, key := range []string{"STRIPE_API_KEY", "LOG_LEVEL"} {
			if !strings.Contains(stdout.String(), key) {
				t.Errorf("stdout = %q, want %s", stdout.String(), key)
			}
		}
	})

	t.Run("validates every pair before writing any", func(t *testing.T) {
		project := setUpEnvFixture(t)
		var stdout, stderr bytes.Buffer
		err := runEnvSetPairs(context.Background(), newTestDependencies(), project.Root, []envSetPair{
			{key: "STRIPE_API_KEY", value: "sk_live_secret"},
			{key: "SITE_HOSTNAME", value: "acme.example"},
		}, envOptions{}, nil, &stdout, &stderr)
		if err == nil {
			t.Fatal("runEnvSetPairs() = nil, want an undeclared key refusal")
		}
		if sent := clitest.RequestsTo[*variablestorev1.SetValueRequest](t, project.Requests, variablestorev1connect.VariableStoreServiceSetValueProcedure); len(sent) != 0 {
			t.Errorf("the CLI sent %d writes, want none", len(sent))
		}
		stored, err := valuesOf(project).List(context.Background(), variablestore.Scope{Project: clitest.FixtureSlug, Tier: environment.TierProduction})
		if err != nil {
			t.Fatalf("list the stored values: %v", err)
		}
		if len(stored) != 0 {
			t.Errorf("stored = %+v, want no writes", stored)
		}
	})
}

func TestTheEnvCommandsOfferOnlyTheFlagsTheyHonour(t *testing.T) {
	t.Parallel()

	t.Run("history offers no --reveal flag where get still does", func(t *testing.T) {
		t.Parallel()

		cmd := NewCommand(newTestDependencies())
		history, _, _ := cmd.Find([]string{"history"})
		get, _, _ := cmd.Find([]string{"get"})
		if f := history.Flags().Lookup("reveal"); f != nil {
			t.Errorf("`ocel env history` registers --reveal (%q); history is metadata only", f.Usage)
		}
		if get.Flags().Lookup("reveal") == nil {
			t.Error("`ocel env get` lost --reveal; reading one named value back is the surface history's removal relies on")
		}
	})

	t.Run("address a named environment", func(t *testing.T) {
		t.Parallel()

		cmd := NewCommand(newTestDependencies())
		for _, name := range []string{"set", "get", "rm", "history"} {
			c, _, _ := cmd.Find([]string{name})
			if c.Flags().Lookup("environment") == nil {
				t.Errorf("`ocel env %s` cannot address a named environment's override", c.Name())
			}
		}
	})
}

func TestRevealingASecretNeedsAnExplicitYes(t *testing.T) {
	t.Run("--reveal alone will not print a secret", func(t *testing.T) {
		root := setUpEnvFixture(t).Root
		envSet(t, root, "STRIPE_API_KEY", "sk_live_secret", envOptions{})

		var stdout, stderr bytes.Buffer
		err := runEnvGet(context.Background(), newStreamedDependencies(&stderr), root, "STRIPE_API_KEY", envOptions{reveal: true}, &stdout, &stderr)
		if err == nil {
			t.Fatal("runEnvGet --reveal on a secret err = nil, want it refused: a secret must not reach stdout on a flag the operator passes out of habit")
		}
		if strings.Contains(stdout.String()+stderr.String()+err.Error(), "sk_live_secret") {
			t.Errorf("output = %q / %q / %v, want no plaintext anywhere in a refusal", stdout.String(), stderr.String(), err)
		}
		for _, want := range []string{"STRIPE_API_KEY", "--yes"} {
			if !strings.Contains(stderr.String(), want) {
				t.Errorf("stream = %q, want %q named", stderr.String(), want)
			}
		}
	})

	t.Run("--reveal --yes prints the secret on stdout and warns on stderr", func(t *testing.T) {
		root := setUpEnvFixture(t).Root
		envSet(t, root, "STRIPE_API_KEY", "sk_live_secret", envOptions{})

		var stdout, stderr bytes.Buffer
		if err := runEnvGet(context.Background(), newStreamedDependencies(&stderr), root, "STRIPE_API_KEY", envOptions{reveal: true, yes: true}, &stdout, &stderr); err != nil {
			t.Fatalf("runEnvGet --reveal --yes err = %v; stderr=%s", err, stderr.String())
		}
		if got := strings.TrimSpace(stdout.String()); got != "sk_live_secret" {
			t.Errorf("stdout = %q, want exactly the value so a script can capture it", got)
		}
		if !strings.Contains(stderr.String(), "STRIPE_API_KEY") {
			t.Errorf("stderr = %q, want a warning naming the secret that was printed", stderr.String())
		}
		if strings.Contains(stderr.String(), "sk_live_secret") {
			t.Errorf("stderr = %q, want the warning to name the key, never repeat the plaintext", stderr.String())
		}
	})

	t.Run("--reveal --yes warns through the run, so nothing reaches stderr beside it", func(t *testing.T) {
		root := setUpEnvFixture(t).Root
		envSet(t, root, "STRIPE_API_KEY", "sk_live_secret", envOptions{})

		var stdout, stderr, stream bytes.Buffer
		if err := runEnvGet(context.Background(), newStreamedDependencies(&stream), root, "STRIPE_API_KEY", envOptions{reveal: true, yes: true}, &stdout, &stderr); err != nil {
			t.Fatalf("runEnvGet --reveal --yes err = %v; stream=%s", err, stream.String())
		}
		if stderr.Len() != 0 {
			t.Errorf("stderr = %q, want nothing written around the run", stderr.String())
		}
		if !strings.Contains(stream.String(), "WARN  [check] STRIPE_API_KEY") {
			t.Errorf("stream = %q, want the warning as a WARN line of the run naming the secret", stream.String())
		}
	})

	t.Run("a plain value needs no acknowledgement and warns about nothing", func(t *testing.T) {
		root := setUpEnvFixture(t).Root
		envSet(t, root, "LOG_LEVEL", "debug", envOptions{})

		var stdout, stderr bytes.Buffer
		if err := runEnvGet(context.Background(), newStreamedDependencies(&stderr), root, "LOG_LEVEL", envOptions{reveal: true}, &stdout, &stderr); err != nil {
			t.Fatalf("runEnvGet --reveal on a plain value err = %v; stderr=%s", err, stderr.String())
		}
		if got := strings.TrimSpace(stdout.String()); got != "debug" {
			t.Errorf("stdout = %q, want %q", got, "debug")
		}
		if strings.Contains(stderr.String(), "secret") {
			t.Errorf("stderr = %q, want no secrecy warning over a value that is not a secret", stderr.String())
		}
	})
}

func TestEnvWritesQuoteTheVersionTheyRead(t *testing.T) {
	t.Run("a set is refused when another write landed between the read and the write", func(t *testing.T) {
		project := setUpEnvFixture(t)
		root := project.Root
		envSet(t, root, "LOG_LEVEL", "info", envOptions{})
		moveBeforeTheNextWrite(project, "LOG_LEVEL")

		var stdout, stderr bytes.Buffer
		err := runEnvSet(context.Background(), newStreamedDependencies(&stderr), root, "LOG_LEVEL", "debug", envOptions{}, nil, &stdout, &stderr)
		if err == nil {
			t.Fatal("runEnvSet over a value somebody else moved err = nil, want a refusal: two operators racing must not overwrite each other silently")
		}
		for _, want := range []string{"LOG_LEVEL", "ocel env get"} {
			if !strings.Contains(stderr.String(), want) {
				t.Errorf("stream = %q, want %q named so the operator can re-read and retry", stderr.String(), want)
			}
		}

		if got := strings.TrimSpace(envGet(t, root, "LOG_LEVEL", envOptions{reveal: true})); got == "debug" {
			t.Errorf("value = %q, want the write that landed first kept: a refused set must not land", got)
		}
	})

	t.Run("an rm is refused when another write landed between the read and the delete", func(t *testing.T) {
		project := setUpEnvFixture(t)
		root := project.Root
		envSet(t, root, "LOG_LEVEL", "info", envOptions{})
		moveBeforeTheNextWrite(project, "LOG_LEVEL")

		var stdout, stderr bytes.Buffer
		err := runEnvRemove(context.Background(), newStreamedDependencies(&stderr), root, "LOG_LEVEL", envOptions{}, &stdout, &stderr)
		if err == nil {
			t.Fatal("runEnvRemove over a value somebody else moved err = nil, want a refusal: the operator would be deleting a value they never saw")
		}
		if !strings.Contains(stderr.String(), "LOG_LEVEL") {
			t.Errorf("stream = %q, want it to name the key", stderr.String())
		}

		if got := strings.TrimSpace(envGet(t, root, "LOG_LEVEL", envOptions{reveal: true})); got == "" {
			t.Errorf("value = %q, want the cell still set: a refused rm must not land", got)
		}
	})

	t.Run("an uncontested set and rm land on the version they read", func(t *testing.T) {
		root := setUpEnvFixture(t).Root
		envSet(t, root, "LOG_LEVEL", "info", envOptions{})
		if out := envSet(t, root, "LOG_LEVEL", "debug", envOptions{}); !strings.Contains(out, "version 2") {
			t.Errorf("set stdout = %q, want version 2: the write quoted version 1 and moved it on", out)
		}

		var stdout, stderr bytes.Buffer
		if err := runEnvRemove(context.Background(), newStreamedDependencies(&stderr), root, "LOG_LEVEL", envOptions{}, &stdout, &stderr); err != nil {
			t.Fatalf("runEnvRemove err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		if !strings.Contains(stdout.String(), "Removed LOG_LEVEL") {
			t.Errorf("rm stdout = %q, want the removal reported", stdout.String())
		}
	})
}

func TestWhatTheDeclarationCollectorPrintsReachesTheRunAsOutputAndNeverRawStderr(t *testing.T) {
	root := setUpDeclaringProject(t, `console.error("collecting the declared variables");`+envDeclaringScript(fixtureDefinitions)).Root
	dependencies := newTestDependencies()
	dependencies.Presentation = func(io.Writer) terminal.Presentation {
		return terminal.Resolve(terminal.Conditions{Format: terminal.FormatJSON})
	}

	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stderr)
	if err := runEnvList(context.Background(), dependencies, root, envOptions{}, &stdout, &stderr); err != nil {
		t.Fatalf("runEnvList err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}

	evs := clitest.RunEvents(t, stderr.String())
	said := slices.IndexFunc(evs, func(ev *streamv1.RunEvent) bool {
		return ev.GetOperation().GetOutput() != nil && strings.Contains(ev.GetOperation().GetMessage(), "collecting the declared variables")
	})
	if said < 0 {
		t.Fatalf("the collector's line never reached the run as output: %s", stderr.String())
	}
	if evs[said].GetOperation().GetPhase() != progressv1.Phase_PHASE_BUILD {
		t.Errorf("the collector's line is in %v, want the build phase that ran it", evs[said].GetOperation().GetPhase())
	}
}

func setUpInlineBindingFixture(t *testing.T) string {
	t.Helper()
	root := setUpEnvFixture(t).Root
	clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), `
export default {
  slug: "`+clitest.FixtureSlug+`",
  provider: { fake: {} },
  domains: { preview: "*.preview.acme.com" },
  bindings: { postgres: { main: { url: { $env: "MAIN_DATABASE_URL" } } } },
};
`)
	return root
}

func TestRunEnvSetTakesAVariableABindingReads(t *testing.T) {
	t.Run("sets it at the project root", func(t *testing.T) {
		root := setUpInlineBindingFixture(t)
		if out := envSet(t, root, "MAIN_DATABASE_URL", "postgres://u:p@db/main", envOptions{}); !strings.Contains(out, "MAIN_DATABASE_URL") {
			t.Errorf("set stdout = %q, want it to name the key it set", out)
		}
	})

	t.Run("refuses it in a folder, since the binding reads the root value", func(t *testing.T) {
		root := setUpInlineBindingFixture(t)
		var stdout, stderr bytes.Buffer
		err := runEnvSet(context.Background(), newStreamedDependencies(&stderr), root, "MAIN_DATABASE_URL", "postgres://u:p@db/main", envOptions{folder: "/web"}, nil, &stdout, &stderr)
		if err == nil {
			t.Fatal("runEnvSet --folder err = nil, want a folder value for a binding's variable refused")
		}
		for _, want := range []string{"MAIN_DATABASE_URL", "bindings.postgres.main", "--folder"} {
			if !strings.Contains(stderr.String(), want) {
				t.Errorf("stream = %q, want %q named", stderr.String(), want)
			}
		}
	})
}

func TestSettingAValueForAnAppOnALiveComputePromisesNoDeploy(t *testing.T) {
	root := setUpComputeFixture(t, provider.ComputeServerless)

	said := envSet(t, root, "API_TOKEN", "sk-live", envOptions{})
	if strings.Contains(said, "next deploy") {
		t.Errorf("`ocel env set` against an app on a compute the provider bakes nothing into said\n%s\nand a live value there is picked up without one", said)
	}
}

func TestSettingAValueForAContainerAppTheProviderReadsLivePromisesNoDeploy(t *testing.T) {
	root := setUpComputeFixture(t, provider.ComputeContainer)

	said := envSet(t, root, "API_TOKEN", "sk-live", envOptions{})
	if strings.Contains(said, "next deploy") {
		t.Errorf("`ocel env set` against a container app said\n%s\nand this provider names no compute it bakes values into, so the running container reads this one without a deploy", said)
	}
}

func setUpComputeFixture(t *testing.T, compute provider.Compute) string {
	t.Helper()
	project := setUpEnvFixture(t)
	project.Provider.WithFacts(func(facts *provider.Facts) { facts.Computes = []provider.Compute{compute} })
	return project.Root
}

func envRemove(t *testing.T, root, key string, opts envOptions) string {
	t.Helper()
	var stdout, stderr bytes.Buffer
	if err := runEnvRemove(context.Background(), newStreamedDependencies(&stderr), root, key, opts, &stdout, &stderr); err != nil {
		t.Fatalf("runEnvRemove(%s) err = %v; stdout=%s stderr=%s", key, err, stdout.String(), stderr.String())
	}
	return stdout.String()
}

func TestRemovingAValueForAnAppOnALiveComputePromisesNoDeploy(t *testing.T) {
	root := setUpComputeFixture(t, provider.ComputeServerless)
	envSet(t, root, "API_TOKEN", "sk-live", envOptions{})

	said := envRemove(t, root, "API_TOKEN", envOptions{})
	if strings.Contains(said, "next deploy") {
		t.Errorf("`ocel env rm` against an app on a compute the provider bakes nothing into said\n%s\nand a removed value stops being read there without one", said)
	}
}

func TestRemovingAValueForAContainerAppTheProviderReadsLivePromisesNoDeploy(t *testing.T) {
	root := setUpComputeFixture(t, provider.ComputeContainer)
	envSet(t, root, "API_TOKEN", "sk-live", envOptions{})

	said := envRemove(t, root, "API_TOKEN", envOptions{})
	if strings.Contains(said, "next deploy") {
		t.Errorf("`ocel env rm` against a container app said\n%s\nand this provider names no compute it bakes values into, so the running container stops reading this one without a deploy", said)
	}
}

var variablesKey = provider.Feature{Name: provider.FeatureVariablesKey, Summary: "a key the variables are sealed under"}

func setUpVariablesKeyFixture(t *testing.T) clitest.FakeProject {
	t.Helper()
	project := setUpEnvFixture(t)
	project.Provider.FakeBootstrap().Offers(variablesKey)
	return project
}

func bootstrapsRequested(t *testing.T, project clitest.FakeProject) []*contractv1.BootstrapRequest {
	t.Helper()
	return clitest.RequestsTo[*contractv1.BootstrapRequest](t, project.Requests, contractv1connect.ProviderServiceBootstrapProcedure)
}

func TestAWriteWithoutTheVariablesKeyOffersTheBootstrapThatAddsIt(t *testing.T) {
	t.Run("a write with no key to seal under names the bootstrap that adds one", func(t *testing.T) {
		root := setUpVariablesKeyFixture(t).Root

		var stdout, stderr bytes.Buffer
		err := runEnvSet(context.Background(), newStreamedDependencies(&stderr), root, "LOG_LEVEL", "debug", envOptions{}, nil, &stdout, &stderr)
		if err == nil {
			t.Fatalf("runEnvSet err = nil, want it refused for want of a key; stdout=%s stderr=%s", stdout.String(), stderr.String())
		}
		if want := provider.FeatureVariablesKey; !strings.Contains(stderr.String(), want) {
			t.Errorf("stream = %q, want it to name %s", stderr.String(), want)
		}
		if want := "ocel bootstrap production --features"; !strings.Contains(stderr.String(), want) {
			t.Errorf("stream = %q, want it to name `%s`", stderr.String(), want)
		}
	})

	t.Run("a read asks for no key at all", func(t *testing.T) {
		root := setUpVariablesKeyFixture(t).Root

		var stdout, stderr bytes.Buffer
		if err := runEnvList(context.Background(), newStreamedDependencies(&stderr), root, envOptions{}, &stdout, &stderr); err != nil {
			t.Fatalf("runEnvList err = %v, want a read to go through a bootstrap with no key; stderr=%s", err, stderr.String())
		}
	})

	t.Run("a write goes through where the key is installed", func(t *testing.T) {
		project := setUpVariablesKeyFixture(t)
		clitest.Bootstrap(t, project.Provider, environment.TierProduction, provider.FeatureVariablesKey)

		var stdout, stderr bytes.Buffer
		if err := runEnvSet(context.Background(), newStreamedDependencies(&stderr), project.Root, "LOG_LEVEL", "debug", envOptions{}, nil, &stdout, &stderr); err != nil {
			t.Fatalf("runEnvSet err = %v, want the write to land; stderr=%s", err, stderr.String())
		}
		if !strings.Contains(stdout.String(), "Set LOG_LEVEL") {
			t.Errorf("stdout = %q, want the write reported", stdout.String())
		}
	})

	t.Run("a write asks for the key and for nothing else the bootstrap lacks", func(t *testing.T) {
		project := setUpEnvFixture(t)
		project.Provider.FakeBootstrap().Offers(append(project.Provider.FakeBootstrap().Catalogue(), variablesKey)...)

		var stdout, stderr bytes.Buffer
		err := runEnvSet(context.Background(), newStreamedDependencies(&stderr), project.Root, "LOG_LEVEL", "debug", envOptions{}, nil, &stdout, &stderr)
		if err == nil {
			t.Fatalf("runEnvSet err = nil, want it refused for want of a key; stdout=%s stderr=%s", stdout.String(), stderr.String())
		}
		if want := "ocel bootstrap production --features " + provider.FeatureVariablesKey; !strings.Contains(stderr.String(), want) {
			t.Errorf("stream = %q, want it to name `%s` alone", stderr.String(), want)
		}
		if strings.Contains(err.Error(), fake.FeatureImages) {
			t.Errorf("runEnvSet err = %v, want a write to ask for the key it seals under, not for what a deploy would need", err)
		}
	})

	t.Run("removing a value seals nothing, so it asks for no key", func(t *testing.T) {
		root := setUpVariablesKeyFixture(t).Root

		var stdout, stderr bytes.Buffer
		if err := runEnvRemove(context.Background(), newStreamedDependencies(&stderr), root, "STRIPE_API_KEY", envOptions{}, &stdout, &stderr); err != nil {
			t.Fatalf("runEnvRemove err = %v, want a removal to go through a bootstrap with no key; stderr=%s", err, stderr.String())
		}
	})

	t.Run("pointing a value at another seals nothing, so it asks for no key", func(t *testing.T) {
		root := setUpVariablesKeyFixture(t).Root

		var stdout, stderr bytes.Buffer
		ref := envRefOptions{project: "platform"}
		if err := runEnvRef(context.Background(), newStreamedDependencies(&stderr), root, "STRIPE_API_KEY", envOptions{}, ref, &stdout, &stderr); err != nil {
			t.Fatalf("runEnvRef err = %v, want a reference to go through a bootstrap with no key; stderr=%s", err, stderr.String())
		}
	})

	t.Run("a terminal is offered the key and the bootstrap runs where it is taken", func(t *testing.T) {
		project := setUpVariablesKeyFixture(t)
		dependencies := newTestDependencies()
		dependencies.StdinIsTerminal = func(io.Reader) bool { return true }
		dependencies.Setups = prerequisite.Setups{prerequisite.Bootstrap: bootstrap.NewSetup()}

		var stdout, stderr bytes.Buffer
		err := runEnvSet(context.Background(), dependencies, project.Root, "LOG_LEVEL", "debug", envOptions{}, strings.NewReader("y\ny\n"), &stdout, &stderr)
		if err != nil {
			t.Fatalf("runEnvSet err = %v, want the offer taken and the write landed; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		if want := "Run `ocel bootstrap production --features " + provider.FeatureVariablesKey + "` now?"; !strings.Contains(stderr.String(), want) {
			t.Errorf("stderr = %q, want the offer put to whoever is at the terminal", stderr.String())
		}
		if strings.Contains(stdout.String(), "Run `ocel bootstrap") {
			t.Errorf("stdout = %q, want the offer kept off the stream a script reads", stdout.String())
		}
		requested := bootstrapsRequested(t, project)
		if len(requested) == 0 {
			t.Fatal("the bootstrap the offer accepted never reached the provider")
		}
		if got := requested[len(requested)-1].GetFeatures(); !slices.Equal(got, []string{provider.FeatureVariablesKey}) {
			t.Errorf("the provider was asked for %q, want the key alone", got)
		}
	})
}

func TestAProviderWithoutTheVariablesKeyFeatureIsOfferedNothing(t *testing.T) {
	t.Run("a write against a catalogue that never lists the key goes straight through", func(t *testing.T) {
		root := setUpEnvFixture(t).Root

		var stdout, stderr bytes.Buffer
		if err := runEnvSet(context.Background(), newStreamedDependencies(&stderr), root, "LOG_LEVEL", "debug", envOptions{}, nil, &stdout, &stderr); err != nil {
			t.Fatalf("runEnvSet err = %v, want a provider that has no such feature never asked for it; stderr=%s", err, stderr.String())
		}
		if !strings.Contains(stdout.String(), "Set LOG_LEVEL") {
			t.Errorf("stdout = %q, want the write reported", stdout.String())
		}
	})

	t.Run("a terminal is offered nothing and the provider is left unbootstrapped", func(t *testing.T) {
		project := setUpEnvFixture(t)
		dependencies := newTestDependencies()
		dependencies.StdinIsTerminal = func(io.Reader) bool { return true }

		var stdout, stderr bytes.Buffer
		if err := runEnvSet(context.Background(), dependencies, project.Root, "LOG_LEVEL", "debug", envOptions{}, strings.NewReader("y\n"), &stdout, &stderr); err != nil {
			t.Fatalf("runEnvSet err = %v, want the write to land unbidden; stderr=%s", err, stderr.String())
		}
		if strings.Contains(stderr.String(), "Run `ocel bootstrap") {
			t.Errorf("stderr = %q, want no offer of a feature this provider has no name for", stderr.String())
		}
		if requested := bootstrapsRequested(t, project); len(requested) != 0 {
			t.Errorf("the provider was bootstrapped for a feature it does not offer: %v", requested)
		}
	})
}

func TestGettingAValueAsJSONPrintsItsMetadataAndRevealsAValueOnlyOnRequest(t *testing.T) {
	t.Run("a secret is absent without --reveal", func(t *testing.T) {
		root := setUpEnvFixture(t).Root
		envSet(t, root, "STRIPE_API_KEY", "sk_live_secret", envOptions{folder: "/web"})

		var stdout, stderr bytes.Buffer
		if err := runEnvGet(context.Background(), newJSONDependencies(&stderr), root, "STRIPE_API_KEY", envOptions{folder: "/web"}, &stdout, &stderr); err != nil {
			t.Fatalf("runEnvGet err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}

		got := clitest.DecodeResult(t, stdout.String())
		if got["revealed"] != false {
			t.Errorf("get json revealed = %v, want false", got["revealed"])
		}
		if _, present := got["value"]; present {
			t.Errorf("get json = %v, want no value field without --reveal", got)
		}
		if strings.Contains(stdout.String()+stderr.String(), "sk_live_secret") {
			t.Errorf("stdout = %q, stream = %q, want the secret nowhere", stdout.String(), stderr.String())
		}
		if got["tier"] != "TIER_PRODUCTION" || got["version"] != "1" {
			t.Errorf("get json = %v, want the production tier and version 1", got)
		}
		coordinate, _ := got["coordinate"].(map[string]any)
		for field, want := range map[string]any{"project": clitest.FixtureSlug, "folder": "/web", "key": "STRIPE_API_KEY", "environment": ""} {
			if coordinate[field] != want {
				t.Errorf("get json coordinate %s = %v, want %v", field, coordinate[field], want)
			}
		}
		if got["size"] != "14" {
			t.Errorf("get json size = %v, want the 14 bytes of the value", got["size"])
		}
		if len(clitest.RunEvents(t, stderr.String())) == 0 {
			t.Errorf("stream = %q, want the run's events there", stderr.String())
		}
	})

	t.Run("a plain value appears when revealed", func(t *testing.T) {
		root := setUpEnvFixture(t).Root
		envSet(t, root, "LOG_LEVEL", "debug", envOptions{})

		var stdout, stderr bytes.Buffer
		if err := runEnvGet(context.Background(), newJSONDependencies(&stderr), root, "LOG_LEVEL", envOptions{reveal: true}, &stdout, &stderr); err != nil {
			t.Fatalf("runEnvGet --reveal err = %v; stderr=%s", err, stderr.String())
		}
		got := clitest.DecodeResult(t, stdout.String())
		if got["revealed"] != true || got["value"] != "debug" {
			t.Errorf("get json = %v, want revealed true and the value", got)
		}
	})

	t.Run("a secret appears only when revealed with --yes", func(t *testing.T) {
		root := setUpEnvFixture(t).Root
		envSet(t, root, "STRIPE_API_KEY", "sk_live_secret", envOptions{})

		var refused, refusedStream bytes.Buffer
		if err := runEnvGet(context.Background(), newJSONDependencies(&refusedStream), root, "STRIPE_API_KEY", envOptions{reveal: true}, &refused, &refusedStream); err == nil {
			t.Fatal("runEnvGet --reveal on a secret err = nil, want it refused")
		}
		if refused.Len() != 0 || strings.Contains(refusedStream.String(), "sk_live_secret") {
			t.Errorf("stdout = %q, stream = %q, want nothing printed and no plaintext on a refusal", refused.String(), refusedStream.String())
		}

		var stdout, stderr bytes.Buffer
		if err := runEnvGet(context.Background(), newJSONDependencies(&stderr), root, "STRIPE_API_KEY", envOptions{reveal: true, yes: true}, &stdout, &stderr); err != nil {
			t.Fatalf("runEnvGet --reveal --yes err = %v; stderr=%s", err, stderr.String())
		}
		got := clitest.DecodeResult(t, stdout.String())
		if got["revealed"] != true || got["value"] != "sk_live_secret" {
			t.Errorf("get json = %v, want revealed true and the secret", got)
		}
		if strings.Contains(stderr.String(), "sk_live_secret") {
			t.Errorf("stream = %q, want the plaintext only on stdout", stderr.String())
		}
	})

	t.Run("a reference names its target as a coordinate", func(t *testing.T) {
		project := setUpEnvFixture(t)
		ownedElsewhere(t, project, "LOG_LEVEL", "warn")
		envRef(t, project.Root, "LOG_LEVEL", envOptions{}, envRefOptions{project: "platform"})

		var stdout, stderr bytes.Buffer
		if err := runEnvGet(context.Background(), newJSONDependencies(&stderr), project.Root, "LOG_LEVEL", envOptions{}, &stdout, &stderr); err != nil {
			t.Fatalf("runEnvGet err = %v; stderr=%s", err, stderr.String())
		}
		got := clitest.DecodeResult(t, stdout.String())
		target, _ := got["target"].(map[string]any)
		if target["project"] != "platform" || target["key"] != "LOG_LEVEL" || target["folder"] != "" {
			t.Errorf("get json target = %v, want platform's LOG_LEVEL as a coordinate", got["target"])
		}
		if _, present := got["value"]; present {
			t.Errorf("get json = %v, want no value without --reveal", got)
		}
	})

	t.Run("a value that is not a reference carries no target", func(t *testing.T) {
		root := setUpEnvFixture(t).Root
		envSet(t, root, "LOG_LEVEL", "debug", envOptions{})

		var stdout, stderr bytes.Buffer
		if err := runEnvGet(context.Background(), newJSONDependencies(&stderr), root, "LOG_LEVEL", envOptions{}, &stdout, &stderr); err != nil {
			t.Fatalf("runEnvGet err = %v", err)
		}
		if got := clitest.DecodeResult(t, stdout.String()); got["target"] != nil {
			t.Errorf("get json target = %v, want none", got["target"])
		}
	})

	t.Run("an unset key leaves the error document to the root", func(t *testing.T) {
		root := setUpEnvFixture(t).Root

		var stdout, stderr bytes.Buffer
		if err := runEnvGet(context.Background(), newJSONDependencies(&stderr), root, "NEVER_SET", envOptions{}, &stdout, &stderr); err == nil {
			t.Fatal("runEnvGet on an unset key err = nil, want a failure")
		}
		if stdout.Len() != 0 {
			t.Errorf("stdout = %q, want nothing", stdout.String())
		}
	})
}

func TestHistoryAsJSONListsVersionsNewestFirstAndNeverAValue(t *testing.T) {
	root := setUpEnvFixture(t).Root
	for _, v := range []string{"sk_first", "sk_second", "sk_third"} {
		envSet(t, root, "STRIPE_API_KEY", v, envOptions{})
	}

	var stdout, stderr bytes.Buffer
	if err := runEnvHistory(context.Background(), newJSONDependencies(&stderr), root, "STRIPE_API_KEY", envOptions{reveal: true}, &stdout, &stderr); err != nil {
		t.Fatalf("runEnvHistory err = %v; stderr=%s", err, stderr.String())
	}

	got := clitest.DecodeResult(t, stdout.String())
	if strings.Contains(stdout.String(), "sk_") {
		t.Errorf("stdout = %q, want no plaintext", stdout.String())
	}
	versions, _ := got["versions"].([]any)
	if len(versions) != 3 {
		t.Fatalf("history json versions = %v, want three", got["versions"])
	}
	for i, want := range []struct{ version, size string }{{"3", "8"}, {"2", "9"}, {"1", "8"}} {
		version, _ := versions[i].(map[string]any)
		if version["version"] != want.version || version["size"] != want.size || version["createdAt"] == "" {
			t.Errorf("history json versions[%d] = %v, want version %s of %s bytes with a creation time", i, version, want.version, want.size)
		}
	}
	coordinate, _ := got["coordinate"].(map[string]any)
	if coordinate["key"] != "STRIPE_API_KEY" || got["tier"] != "TIER_PRODUCTION" {
		t.Errorf("history json = %v, want the key and tier it read", got)
	}
	if len(clitest.RunEvents(t, stderr.String())) == 0 {
		t.Errorf("stream = %q, want the run's events there", stderr.String())
	}
}

func TestHistoryAsJSONOfAnUnsetKeyListsNoVersions(t *testing.T) {
	root := setUpEnvFixture(t).Root

	var stdout, stderr bytes.Buffer
	if err := runEnvHistory(context.Background(), newJSONDependencies(&stderr), root, "LOG_LEVEL", envOptions{}, &stdout, &stderr); err != nil {
		t.Fatalf("runEnvHistory err = %v; stderr=%s", err, stderr.String())
	}
	if versions, ok := clitest.DecodeResult(t, stdout.String())["versions"].([]any); !ok || len(versions) != 0 {
		t.Errorf("history json = %q, want an empty versions list", stdout.String())
	}
}
