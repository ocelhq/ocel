package transform

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func fixtureRoot(t *testing.T, modules map[string]string) string {
	t.Helper()
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node is not on PATH")
	}

	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate the test source")
	}
	repo := filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", "..", "..", ".."))
	pkg := filepath.Join(repo, "packages", "provider-aws")
	if _, err := os.Stat(filepath.Join(pkg, "package.json")); err != nil {
		t.Skipf("the provider-aws package is not checked out: %v", err)
	}

	root := t.TempDir()
	scope := filepath.Join(root, "node_modules", "@ocel")
	if err := os.MkdirAll(scope, 0o755); err != nil {
		t.Fatalf("create node_modules: %v", err)
	}
	if err := os.Symlink(pkg, filepath.Join(scope, "provider-aws")); err != nil {
		t.Fatalf("link the provider package: %v", err)
	}
	for name, source := range modules {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
		if err := os.WriteFile(path, []byte(source), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	return root
}

func functionRequest() Request {
	return Request{
		EnvClass: "production",
		Env:      "prod",
		Resources: []Resource{{
			Type: "function",
			Name: "api-users",
			App:  "api",
			Surfaces: Surfaces{
				"lambda": {"memorySizeMb": 1024, "timeoutSeconds": 30, "runtime": "nodejs24.x"},
				"url":    {"invokeMode": "RESPONSE_STREAM"},
			},
		}},
	}
}

func TestNodePassEvaluate(t *testing.T) {
	t.Parallel()

	t.Run("a project naming no transform module never reaches for node", func(t *testing.T) {
		t.Parallel()

		surfaces, err := NodePass{Root: "/nonexistent"}.Evaluate(t.Context(), functionRequest())
		if err != nil {
			t.Fatalf("Evaluate: %v", err)
		}
		if surfaces != nil {
			t.Fatalf("surfaces = %v, want the provider's own args left alone", surfaces)
		}
	})

	t.Run("the listed modules patch the defaulted args in order", func(t *testing.T) {
		t.Parallel()

		root := fixtureRoot(t, map[string]string{
			"infra/defaults.transform.ts": `
				import { defineTransform } from "@ocel/provider-aws/transform"
				export default defineTransform([
					{ function: { lambda: { memorySizeMb: 2048, timeoutSeconds: 60 } } },
					{ if: (ctx) => ctx.envClass === "production", function: { url: { invokeMode: "BUFFERED" } } },
				])
			`,
			"infra/late.transform.ts": `
				import { defineTransform } from "@ocel/provider-aws/transform"
				export default defineTransform({
					function: { lambda: (args, ctx) => ({ ...args, memorySizeMb: ctx.resourceName === "api-users" ? 512 : args.memorySizeMb }) },
				})
			`,
		})

		surfaces, err := NodePass{
			Root:    root,
			Modules: []string{"./infra/defaults.transform.ts", "./infra/late.transform.ts"},
		}.Evaluate(t.Context(), functionRequest())
		if err != nil {
			t.Fatalf("Evaluate: %v", err)
		}

		if got := surfaces[0]["lambda"]["memorySizeMb"]; got != float64(512) {
			t.Errorf("memorySizeMb = %v, want the later module to win with 512", got)
		}
		if got := surfaces[0]["lambda"]["timeoutSeconds"]; got != float64(60) {
			t.Errorf("timeoutSeconds = %v, want 60 from the first module", got)
		}
		if got := surfaces[0]["url"]["invokeMode"]; got != "BUFFERED" {
			t.Errorf("invokeMode = %v, want the production gate to have opened", got)
		}
	})

	t.Run("a gate closed against the ambient context leaves the args alone", func(t *testing.T) {
		t.Parallel()

		root := fixtureRoot(t, map[string]string{
			"infra/preview.transform.ts": `
				import { defineTransform } from "@ocel/provider-aws/transform"
				export default defineTransform({
					if: (ctx) => ctx.envClass === "preview" || ctx.app === "web",
					function: { lambda: { memorySizeMb: 128 } },
				})
			`,
		})

		surfaces, err := NodePass{
			Root:    root,
			Modules: []string{"./infra/preview.transform.ts"},
		}.Evaluate(t.Context(), functionRequest())
		if err != nil {
			t.Fatalf("Evaluate: %v", err)
		}

		if got := surfaces[0]["lambda"]["memorySizeMb"]; got != float64(1024) {
			t.Errorf("memorySizeMb = %v, want the provider's own 1024", got)
		}
	})

	t.Run("a patch outside the allowlist fails the deploy, naming module, resource and field", func(t *testing.T) {
		t.Parallel()

		root := fixtureRoot(t, map[string]string{
			"infra/bad.transform.ts": `
				import { defineTransform } from "@ocel/provider-aws/transform"
				export default defineTransform({
					function: { lambda: { reservedConcurrency: 4 } as never },
				})
			`,
		})

		_, err := NodePass{
			Root:    root,
			Modules: []string{"./infra/bad.transform.ts"},
		}.Evaluate(t.Context(), functionRequest())
		if err == nil {
			t.Fatal("Evaluate succeeded, want the unknown field rejected")
		}
		for _, fact := range []string{"./infra/bad.transform.ts", "function.lambda.reservedConcurrency"} {
			if !strings.Contains(err.Error(), fact) {
				t.Errorf("error = %q, missing %q", err, fact)
			}
		}
	})

	t.Run("a module without a defineTransform default export fails the deploy by name", func(t *testing.T) {
		t.Parallel()

		root := fixtureRoot(t, map[string]string{
			"infra/empty.transform.ts": `export default { function: {} }`,
		})

		_, err := NodePass{
			Root:    root,
			Modules: []string{"./infra/empty.transform.ts"},
		}.Evaluate(t.Context(), functionRequest())
		if err == nil {
			t.Fatal("Evaluate succeeded, want the malformed module rejected")
		}
		if !strings.Contains(err.Error(), "./infra/empty.transform.ts") {
			t.Errorf("error = %q, missing the module that failed", err)
		}
	})
}
