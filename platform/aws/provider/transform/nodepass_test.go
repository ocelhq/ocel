package transform

import (
	"strings"
	"testing"

	"github.com/ocelhq/ocel/platform/aws/provider/transform/transformtest"
)

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

		results, err := NodePass{Root: "/nonexistent"}.Evaluate(t.Context(), functionRequest())
		if err != nil {
			t.Fatalf("Evaluate: %v", err)
		}
		if results != nil {
			t.Fatalf("results = %v, want the provider's own args left alone", results)
		}
	})

	t.Run("the listed modules patch the defaulted args in order", func(t *testing.T) {
		t.Parallel()

		root := transformtest.Root(t, map[string]string{
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

		results, err := NodePass{
			Root:    root,
			Modules: []string{"./infra/defaults.transform.ts", "./infra/late.transform.ts"},
		}.Evaluate(t.Context(), functionRequest())
		if err != nil {
			t.Fatalf("Evaluate: %v", err)
		}

		if got := results[0].Surfaces["lambda"]["memorySizeMb"]; got != float64(512) {
			t.Errorf("memorySizeMb = %v, want the later module to win with 512", got)
		}
		if got := results[0].Surfaces["lambda"]["timeoutSeconds"]; got != float64(60) {
			t.Errorf("timeoutSeconds = %v, want 60 from the first module", got)
		}
		if got := results[0].Surfaces["url"]["invokeMode"]; got != "BUFFERED" {
			t.Errorf("invokeMode = %v, want the production gate to have opened", got)
		}
	})

	t.Run("a gate closed against the ambient context leaves the args alone", func(t *testing.T) {
		t.Parallel()

		root := transformtest.Root(t, map[string]string{
			"infra/preview.transform.ts": `
				import { defineTransform } from "@ocel/provider-aws/transform"
				export default defineTransform({
					if: (ctx) => ctx.envClass === "preview" || ctx.app === "web",
					function: { lambda: { memorySizeMb: 128 } },
				})
			`,
		})

		results, err := NodePass{
			Root:    root,
			Modules: []string{"./infra/preview.transform.ts"},
		}.Evaluate(t.Context(), functionRequest())
		if err != nil {
			t.Fatalf("Evaluate: %v", err)
		}

		if got := results[0].Surfaces["lambda"]["memorySizeMb"]; got != float64(1024) {
			t.Errorf("memorySizeMb = %v, want the provider's own 1024", got)
		}
	})

	t.Run("a patch outside the allowlist fails the deploy, naming module, resource and field", func(t *testing.T) {
		t.Parallel()

		root := transformtest.Root(t, map[string]string{
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

		root := transformtest.Root(t, map[string]string{
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
