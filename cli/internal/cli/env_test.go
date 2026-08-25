package cli

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	envvarsv1 "github.com/ocelhq/ocel/pkg/proto/provider/envvars/v1"

	"github.com/ocelhq/ocel/cli/internal/cli/clitest"
)

func setUpEnvFixture(t *testing.T) string {
	t.Helper()
	root, _ := clitest.SetUpDeployFixture(t)
	t.Setenv(clitest.FakeVarsStoreEnvVar, filepath.Join(t.TempDir(), "vars.json"))
	return root
}

func envSet(t *testing.T, root, key, value string, opts envOptions) string {
	t.Helper()
	var stdout, stderr bytes.Buffer
	if err := runEnvSet(context.Background(), newDeps(), root, key, value, opts, &stdout, &stderr); err != nil {
		t.Fatalf("runEnvSet(%s) err = %v; stdout=%s stderr=%s", key, err, stdout.String(), stderr.String())
	}
	return stdout.String()
}

func seedFakeValue(t *testing.T, tier environmentv1.Tier, c *envvarsv1.Coordinate, value string) {
	t.Helper()
	store, err := clitest.LoadFakeStore()
	if err != nil {
		t.Fatalf("load the fake store: %v", err)
	}
	store[clitest.FakeCoordinateID(tier, c)] = &clitest.FakeCell{
		Tier:       tier,
		Coordinate: clitest.FakeCoordinate{Slug: c.GetSlug(), Folder: c.GetFolder(), Key: c.GetKey(), Environment: c.GetEnvironment()},
		Versions:   []clitest.FakeCellData{{Value: value, Ts: 1_700_000_000}},
	}
	if err := clitest.SaveFakeStore(store); err != nil {
		t.Fatalf("save the fake store: %v", err)
	}
}

func envDeclaringScript(definitions string) string {
	return fmt.Sprintf(`
declare global {
  var __ocelRegister: Promise<unknown>[];
}
globalThis.__ocelRegister ??= [];

globalThis.__ocelRegister.push(
  (async () => {
    const log = process.env.OCEL_TEST_DISCOVERY_LOG;
    if (log) await (await import("node:fs/promises")).appendFile(log, "ran\n");

    const res = await fetch(new URL("/app.resources.v1.ResourceService/DeclareEnv", process.env.OCEL_DEV_SERVER), {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ definitions: %s }),
    });
    if (!res.ok) throw new Error("DeclareEnv failed: " + res.status + " " + (await res.text()));
  })(),
);
export {};
`, definitions)
}

func setUpDeclaringFixture(t *testing.T, definitions string) (root, log string) {
	t.Helper()
	root = setUpEnvGateFixtureWith(t, "[]", envDeclaringScript(definitions))
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

func TestRunEnvSet(t *testing.T) {
	t.Run("names the key it set, and the value it wrote reads back only when revealing is explicit", func(t *testing.T) {
		root := setUpEnvFixture(t)

		if out := envSet(t, root, "STRIPE_API_KEY", "sk_live_secret", envOptions{}); !strings.Contains(out, "STRIPE_API_KEY") {
			t.Errorf("set stdout = %q, want it to name the key it set", out)
		}

		t.Run("the value is withheld without --reveal", func(t *testing.T) {
			var plain bytes.Buffer
			if err := runEnvGet(context.Background(), newDeps(), root, "STRIPE_API_KEY", envOptions{}, &plain, &plain); err != nil {
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
			if err := runEnvGet(context.Background(), newDeps(), root, "STRIPE_API_KEY", envOptions{reveal: true}, &revealed, &revealed); err != nil {
				t.Fatalf("runEnvGet --reveal err = %v; out=%s", err, revealed.String())
			}
			if strings.TrimSpace(revealed.String()) != "sk_live_secret" {
				t.Errorf("get --reveal stdout = %q, want exactly the value so it is scriptable", revealed.String())
			}
		})
	})

	t.Run("an override is its own cell beside the value bound to all environments", func(t *testing.T) {
		root := setUpEnvFixture(t)
		t.Setenv(clitest.FakeInfraTierEnvVar, "preview")

		preview := envOptions{preview: true}
		staging := envOptions{preview: true, environment: "staging"}
		envSet(t, root, "STRIPE_API_KEY", "sk_shared", preview)
		envSet(t, root, "STRIPE_API_KEY", "sk_staging", staging)

		for name, tc := range map[string]struct {
			opts envOptions
			want string
		}{
			"the environment holding the override": {opts: staging, want: "sk_staging"},
			"every other environment":              {opts: preview, want: "sk_shared"},
		} {
			t.Run(name, func(t *testing.T) {
				opts := tc.opts
				opts.reveal = true
				var stdout bytes.Buffer
				if err := runEnvGet(context.Background(), newDeps(), root, "STRIPE_API_KEY", opts, &stdout, &stdout); err != nil {
					t.Fatalf("runEnvGet err = %v; out=%s", err, stdout.String())
				}
				if got := strings.TrimSpace(stdout.String()); got != tc.want {
					t.Errorf("value = %q, want %q", got, tc.want)
				}
			})
		}
	})

	t.Run("refuses an environment that does not exist, and the refused write does not land", func(t *testing.T) {
		root := setUpEnvFixture(t)
		t.Setenv(clitest.FakeInfraTierEnvVar, "preview")

		var stdout, stderr bytes.Buffer
		err := runEnvSet(context.Background(), newDeps(), root, "STRIPE_API_KEY", "sk_typo", envOptions{preview: true, environment: "stagng"}, &stdout, &stderr)
		if err == nil {
			t.Fatal("runEnvSet against an environment that does not exist err = nil, want a refusal")
		}
		if !strings.Contains(err.Error(), "stagng") || !strings.Contains(err.Error(), "staging") {
			t.Errorf("err = %v, want it to name what was asked for and what exists", err)
		}

		var get bytes.Buffer
		if err := runEnvGet(context.Background(), newDeps(), root, "STRIPE_API_KEY", envOptions{preview: true, environment: "stagng", reveal: true}, &get, &get); err == nil {
			t.Errorf("the refused write landed anyway: get = %q", get.String())
		}
	})

	t.Run("refuses an environment on production", func(t *testing.T) {
		root := setUpEnvFixture(t)

		var stdout, stderr bytes.Buffer
		err := runEnvSet(context.Background(), newDeps(), root, "STRIPE_API_KEY", "sk_live", envOptions{environment: "staging"}, &stdout, &stderr)
		if err == nil {
			t.Fatal("runEnvSet --environment against production err = nil, want a refusal")
		}
		if !strings.Contains(err.Error(), "--preview") {
			t.Errorf("err = %v, want it to name the flag that selects the bootstrap overrides live on", err)
		}
	})

	t.Run("refuses on preview infrastructure", func(t *testing.T) {
		root := setUpEnvFixture(t)
		t.Setenv(clitest.FakeInfraTierEnvVar, "preview")

		var stdout, stderr bytes.Buffer
		err := runEnvSet(context.Background(), newDeps(), root, "STRIPE_API_KEY", "sk_live_secret", envOptions{}, &stdout, &stderr)
		if err == nil {
			t.Fatal("runEnvSet against preview infrastructure err = nil, want a class-mismatch refusal")
		}
	})

	t.Run("refuses a root value for a scoped key", func(t *testing.T) {
		root := setUpEnvGateFixture(t, `[{"key":"POSTHOG_ID","class":"VARIABLE_CLASS_PLAIN","required":true,"folders":["/web","/admin"]}]`)

		var stdout, stderr bytes.Buffer
		err := runEnvSet(context.Background(), newDeps(), root, "POSTHOG_ID", "ph_root", envOptions{}, &stdout, &stderr)
		if err == nil {
			t.Fatal("runEnvSet err = nil, want a root value for a scoped key refused: nothing could ever read it")
		}
		for _, want := range []string{"POSTHOG_ID", "/web", "/admin"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("err = %v, want it to name %q", err, want)
			}
		}
	})

	t.Run("refuses a scoped key in a folder it does not name", func(t *testing.T) {
		root := setUpEnvGateFixture(t, `[{"key":"POSTHOG_ID","class":"VARIABLE_CLASS_PLAIN","required":true,"folders":["/web"]}]`)

		var stdout, stderr bytes.Buffer
		err := runEnvSet(context.Background(), newDeps(), root, "POSTHOG_ID", "ph", envOptions{folder: "/admin"}, &stdout, &stderr)
		if err == nil {
			t.Fatal("runEnvSet err = nil, want a folder outside the key's scope refused")
		}
		if !strings.Contains(err.Error(), "/admin") {
			t.Errorf("err = %v, want it to name the folder it refused", err)
		}
	})

	t.Run("accepts a scoped key in a folder it names", func(t *testing.T) {
		root := setUpEnvGateFixture(t, `[{"key":"POSTHOG_ID","class":"VARIABLE_CLASS_PLAIN","required":true,"folders":["/web"]}]`)

		if out := envSet(t, root, "POSTHOG_ID", "ph_web", envOptions{folder: "/web"}); !strings.Contains(out, "/web") {
			t.Errorf("set stdout = %q, want the folder it wrote named", out)
		}
	})

	t.Run("leaves an unscoped key writable at root and in a folder", func(t *testing.T) {
		root := setUpEnvGateFixture(t, `[{"key":"LOG_LEVEL","class":"VARIABLE_CLASS_PLAIN","required":true}]`)

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
		if err := runEnvGet(context.Background(), newDeps(), root, "POSTHOG_ID", envOptions{folder: "/web", reveal: true}, &out, &out); err != nil {
			t.Fatalf("runEnvGet err = %v; out=%s", err, out.String())
		}
		if strings.TrimSpace(out.String()) != "ph_two" {
			t.Errorf("value in /web = %q, want %q: the second write must land like the first", out.String(), "ph_two")
		}
	})

	t.Run("picks up a scope the code gained since the last write", func(t *testing.T) {
		root, log := setUpDeclaringFixture(t, `[{"key":"POSTHOG_ID","class":"VARIABLE_CLASS_PLAIN","required":true}]`)

		envSet(t, root, "POSTHOG_ID", "ph_root", envOptions{})

		clitest.WriteFile(t, filepath.Join(root, "ocel", "env.ts"),
			envDeclaringScript(`[{"key":"POSTHOG_ID","class":"VARIABLE_CLASS_PLAIN","required":true,"folders":["/web"]}]`))

		var stdout, stderr bytes.Buffer
		err := runEnvSet(context.Background(), newDeps(), root, "POSTHOG_ID", "ph_root_again", envOptions{}, &stdout, &stderr)
		if err == nil {
			t.Fatal("runEnvSet err = nil, want the scope the code now declares to refuse a root write")
		}
		if !strings.Contains(err.Error(), "/web") {
			t.Errorf("err = %v, want it to name the folder the key is now scoped to", err)
		}
		if got := discoveryRuns(t, log); got != 2 {
			t.Errorf("discovery ran %d times, want 2: the declaring code changed between the writes", got)
		}
	})

	t.Run("does not trust a cached absence for a conditionally scoped key", func(t *testing.T) {
		root := setUpEnvGateFixtureWith(t, "[]", envDeclareOnlyScript)

		envSet(t, root, "LOG_LEVEL", "info", envOptions{})

		t.Setenv("OCEL_TEST_ENV_DEFINITIONS", `[{"key":"POSTHOG_ID","class":"VARIABLE_CLASS_PLAIN","required":true,"folders":["/web"]}]`)

		var stdout, stderr bytes.Buffer
		err := runEnvSet(context.Background(), newDeps(), root, "POSTHOG_ID", "ph_root", envOptions{}, &stdout, &stderr)
		if err == nil {
			t.Fatal("runEnvSet err = nil, want a root value for a scoped key refused: a cached set that never mentioned the key cannot say it is unscoped")
		}
		if !strings.Contains(err.Error(), "/web") {
			t.Errorf("err = %v, want it to name the folder the key is scoped to", err)
		}

		var out bytes.Buffer
		if err := runEnvGet(context.Background(), newDeps(), root, "POSTHOG_ID", envOptions{reveal: true}, &out, &out); err == nil {
			t.Errorf("runEnvGet at root err = nil (out=%q), want no root cell written", out.String())
		}
	})
}

func TestRunEnvGet(t *testing.T) {
	t.Run("reports an unset key", func(t *testing.T) {
		root := setUpEnvFixture(t)

		var stdout, stderr bytes.Buffer
		err := runEnvGet(context.Background(), newDeps(), root, "NEVER_SET", envOptions{reveal: true}, &stdout, &stderr)
		if err == nil {
			t.Fatal("runEnvGet on an unset key err = nil, want a failure rather than an empty value")
		}
		if !strings.Contains(err.Error(), "NEVER_SET") {
			t.Errorf("runEnvGet err = %v, want it to name the key", err)
		}
	})

	t.Run("a folder and the root are separate cells", func(t *testing.T) {
		root := setUpEnvFixture(t)
		envSet(t, root, "POSTHOG_ID", "web-id", envOptions{folder: "/web"})

		var stdout, stderr bytes.Buffer
		if err := runEnvGet(context.Background(), newDeps(), root, "POSTHOG_ID", envOptions{reveal: true}, &stdout, &stderr); err == nil {
			t.Fatalf("runEnvGet at root err = nil (out=%q), want the root cell to be unset", stdout.String())
		}

		var folder bytes.Buffer
		if err := runEnvGet(context.Background(), newDeps(), root, "POSTHOG_ID", envOptions{folder: "/web", reveal: true}, &folder, &folder); err != nil {
			t.Fatalf("runEnvGet in /web err = %v", err)
		}
		if strings.TrimSpace(folder.String()) != "web-id" {
			t.Errorf("get in /web = %q, want %q", folder.String(), "web-id")
		}
	})

	t.Run("production and preview are separate stores", func(t *testing.T) {
		root := setUpEnvFixture(t)
		envSet(t, root, "STRIPE_API_KEY", "sk_live_secret", envOptions{})

		t.Setenv(clitest.FakeInfraTierEnvVar, "preview")

		var get bytes.Buffer
		if err := runEnvGet(context.Background(), newDeps(), root, "STRIPE_API_KEY", envOptions{preview: true, reveal: true}, &get, &get); err == nil {
			t.Errorf("preview get err = nil (out=%q), want the production value unreadable from preview", get.String())
		}

		var ls bytes.Buffer
		if err := runEnvLs(context.Background(), newDeps(), root, envOptions{preview: true}, &ls, &ls); err != nil {
			t.Fatalf("runEnvLs --preview err = %v; out=%s", err, ls.String())
		}
		if strings.Contains(ls.String(), "STRIPE_API_KEY") {
			t.Errorf("preview ls = %q, want no production value listed", ls.String())
		}

		envSet(t, root, "STRIPE_API_KEY", "sk_test_preview", envOptions{preview: true})

		t.Setenv(clitest.FakeInfraTierEnvVar, "production")

		var production bytes.Buffer
		if err := runEnvGet(context.Background(), newDeps(), root, "STRIPE_API_KEY", envOptions{reveal: true}, &production, &production); err != nil {
			t.Fatalf("runEnvGet err = %v; out=%s", err, production.String())
		}
		if got := strings.TrimSpace(production.String()); got != "sk_live_secret" {
			t.Errorf("production value = %q, want %q: a preview write must not reach production", got, "sk_live_secret")
		}
	})
}

func TestRunEnvLs(t *testing.T) {
	t.Run("shows keys and metadata but never values", func(t *testing.T) {
		root := setUpEnvFixture(t)
		envSet(t, root, "STRIPE_API_KEY", "sk_live_secret", envOptions{})
		envSet(t, root, "POSTHOG_ID", "ph_public_id", envOptions{folder: "/web"})

		var stdout, stderr bytes.Buffer
		if err := runEnvLs(context.Background(), newDeps(), root, envOptions{}, &stdout, &stderr); err != nil {
			t.Fatalf("runEnvLs err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}

		out := stdout.String()
		for _, want := range []string{"STRIPE_API_KEY", "POSTHOG_ID", "/web"} {
			if !strings.Contains(out, want) {
				t.Errorf("ls stdout = %q, want it to show %q", out, want)
			}
		}
		for _, secret := range []string{"sk_live_secret", "ph_public_id"} {
			if strings.Contains(out, secret) {
				t.Errorf("ls stdout = %q, want no value printed (found %q)", out, secret)
			}
		}
	})

	t.Run("reports an empty store", func(t *testing.T) {
		root := setUpEnvFixture(t)

		var stdout, stderr bytes.Buffer
		if err := runEnvLs(context.Background(), newDeps(), root, envOptions{}, &stdout, &stderr); err != nil {
			t.Fatalf("runEnvLs err = %v; stderr=%s", err, stderr.String())
		}
		if !strings.Contains(stdout.String(), "ocel env set") {
			t.Errorf("ls on an empty store = %q, want it to name the command that fills it", stdout.String())
		}
	})

	t.Run("a production override is orphaned though a preview shares its name", func(t *testing.T) {
		root := setUpEnvFixture(t)
		seedFakeValue(t, environmentv1.Tier_TIER_PRODUCTION,
			&envvarsv1.Coordinate{Slug: "test-app", Key: "STRIPE_API_KEY", Environment: "staging"}, "sk_stray")
		t.Setenv(clitest.FakeEnvironmentsEnvVar, "staging")

		var ls bytes.Buffer
		if err := runEnvLs(context.Background(), newDeps(), root, envOptions{}, &ls, &ls); err != nil {
			t.Fatalf("runEnvLs err = %v; out=%s", err, ls.String())
		}
		if !strings.Contains(ls.String(), "orphaned") {
			t.Errorf("ls = %q, want the production row marked orphaned: no production function reads a named environment", ls.String())
		}
	})
}

func TestRunEnvRm(t *testing.T) {
	t.Run("removes the value", func(t *testing.T) {
		root := setUpEnvFixture(t)
		envSet(t, root, "STRIPE_API_KEY", "sk_live_secret", envOptions{})

		var stdout, stderr bytes.Buffer
		if err := runEnvRm(context.Background(), newDeps(), root, "STRIPE_API_KEY", envOptions{}, &stdout, &stderr); err != nil {
			t.Fatalf("runEnvRm err = %v; stderr=%s", err, stderr.String())
		}
		if !strings.Contains(stdout.String(), "STRIPE_API_KEY") {
			t.Errorf("rm stdout = %q, want it to name the removed key", stdout.String())
		}

		var after bytes.Buffer
		if err := runEnvLs(context.Background(), newDeps(), root, envOptions{}, &after, &after); err != nil {
			t.Fatalf("runEnvLs err = %v", err)
		}
		if strings.Contains(after.String(), "STRIPE_API_KEY") {
			t.Errorf("ls after rm = %q, want the value gone", after.String())
		}
	})

	t.Run("reports nothing to remove", func(t *testing.T) {
		root := setUpEnvFixture(t)

		var stdout, stderr bytes.Buffer
		if err := runEnvRm(context.Background(), newDeps(), root, "NEVER_SET", envOptions{}, &stdout, &stderr); err != nil {
			t.Fatalf("runEnvRm err = %v; stderr=%s", err, stderr.String())
		}
		if !strings.Contains(stdout.String(), "No value") {
			t.Errorf("rm of an unset key = %q, want it to say there was nothing set", stdout.String())
		}
	})

	t.Run("an orphaned override is listed and removable", func(t *testing.T) {
		root := setUpEnvFixture(t)
		t.Setenv(clitest.FakeInfraTierEnvVar, "preview")
		envSet(t, root, "STRIPE_API_KEY", "sk_staging", envOptions{preview: true, environment: "staging"})

		t.Setenv(clitest.FakeEnvironmentsEnvVar, "none")

		var ls bytes.Buffer
		if err := runEnvLs(context.Background(), newDeps(), root, envOptions{preview: true}, &ls, &ls); err != nil {
			t.Fatalf("runEnvLs err = %v; out=%s", err, ls.String())
		}
		if !strings.Contains(ls.String(), "orphaned") {
			t.Errorf("ls = %q, want the override marked orphaned once its environment is gone", ls.String())
		}

		var rm bytes.Buffer
		if err := runEnvRm(context.Background(), newDeps(), root, "STRIPE_API_KEY", envOptions{preview: true, environment: "staging"}, &rm, &rm); err != nil {
			t.Fatalf("runEnvRm err = %v; out=%s", err, rm.String())
		}
		if !strings.Contains(rm.String(), "Removed") {
			t.Errorf("rm = %q, want the orphan removed rather than reported unset", rm.String())
		}

		var after bytes.Buffer
		if err := runEnvLs(context.Background(), newDeps(), root, envOptions{preview: true}, &after, &after); err != nil {
			t.Fatalf("runEnvLs err = %v; out=%s", err, after.String())
		}
		if strings.Contains(after.String(), "STRIPE_API_KEY") {
			t.Errorf("ls = %q, want the removed orphan gone from the listing", after.String())
		}
	})
}

func TestRunEnvHistory(t *testing.T) {
	t.Run("shows metadata newest first and never a plaintext", func(t *testing.T) {
		root := setUpEnvFixture(t)
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
				if err := runEnvHistory(context.Background(), newDeps(), root, "STRIPE_API_KEY", opts, &stdout, &stdout); err != nil {
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

func TestRenderValues(t *testing.T) {
	t.Parallel()

	t.Run("the root folder does not print a rejected slash", func(t *testing.T) {
		t.Parallel()

		var stdout bytes.Buffer
		renderValues(&stdout, []*envvarsv1.ValueMetadata{
			{Coordinate: &envvarsv1.Coordinate{Key: "STRIPE_API_KEY", Folder: ""}},
		}, nil)

		out := stdout.String()
		if !strings.Contains(out, "(project root)") {
			t.Errorf("ls stdout = %q, want the root cell rendered as %q", out, "(project root)")
		}
		for _, line := range strings.Split(out, "\n") {
			for _, f := range strings.Fields(line) {
				if f == "/" {
					t.Errorf("ls stdout = %q, want no field spelled %q: --folder / is rejected", out, "/")
				}
			}
		}
	})

	t.Run("marks an override whose environment is gone", func(t *testing.T) {
		t.Parallel()

		const note = "orphaned"

		var withOrphan bytes.Buffer
		renderValues(&withOrphan, []*envvarsv1.ValueMetadata{
			{Coordinate: &envvarsv1.Coordinate{Key: "STRIPE_API_KEY"}},
			{Coordinate: &envvarsv1.Coordinate{Key: "STRIPE_API_KEY", Environment: "pr-42"}},
			{Coordinate: &envvarsv1.Coordinate{Key: "STRIPE_API_KEY", Environment: "staging"}},
		}, []string{"staging"})

		out := withOrphan.String()
		if !strings.Contains(out, note) {
			t.Errorf("ls stdout = %q, want the override for pr-42 marked orphaned", out)
		}
		if !strings.Contains(out, "ocel env rm") {
			t.Errorf("ls stdout = %q, want it to name the command that removes an orphan", out)
		}
		for _, line := range strings.Split(out, "\n") {
			if strings.Contains(line, "staging") && strings.Contains(line, note) {
				t.Errorf("ls line = %q, want an environment that still exists left unmarked", line)
			}
		}

		var live bytes.Buffer
		renderValues(&live, []*envvarsv1.ValueMetadata{
			{Coordinate: &envvarsv1.Coordinate{Key: "STRIPE_API_KEY", Environment: "staging"}},
		}, []string{"staging"})
		if out := live.String(); strings.Contains(out, note) {
			t.Errorf("ls stdout = %q, want no orphan note when every override has its environment", out)
		}
	})
}

func TestEnvCommands(t *testing.T) {
	t.Parallel()

	t.Run("history offers no --reveal flag where get still does", func(t *testing.T) {
		t.Parallel()

		if f := envHistoryCmd.Flags().Lookup("reveal"); f != nil {
			t.Errorf("`ocel env history` registers --reveal (%q); history is metadata only", f.Usage)
		}
		if envGetCmd.Flags().Lookup("reveal") == nil {
			t.Error("`ocel env get` lost --reveal; reading one named value back is the surface history's removal relies on")
		}
	})

	t.Run("address a named environment", func(t *testing.T) {
		t.Parallel()

		for _, c := range []*cobra.Command{envSetCmd, envGetCmd, envRmCmd, envHistoryCmd} {
			if c.Flags().Lookup("environment") == nil {
				t.Errorf("`ocel env %s` cannot address a named environment's override", c.Name())
			}
		}
	})
}
