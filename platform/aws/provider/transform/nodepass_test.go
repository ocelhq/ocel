package transform

import (
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/platform/aws/provider/transform/transformtest"
)

func functionRequest() Request {
	return Request{
		Provider:  Provider,
		EnvClass:  "production",
		Env:       "prod",
		Resources: []Resource{{Type: "function", Name: "api-users", App: "api"}},
	}
}

func evaluateWith(t *testing.T, req Request, modules map[string]string, listed ...string) ([]Result, error) {
	t.Helper()
	root := transformtest.Root(t, modules)
	return NodePass{Root: root, Modules: listed}.Evaluate(t.Context(), req)
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

	t.Run("the listed modules patch in order, the later one winning", func(t *testing.T) {
		t.Parallel()

		results, err := evaluateWith(t, functionRequest(), map[string]string{
			"modules/defaults.transform.ts": `
				import { defineTransform } from "@ocel/transforms"
				export default defineTransform([
					{ aws: { function: { lambda: { memorySize: 2048, timeout: 60 } } } },
					{ if: (ctx) => ctx.envClass === "production", aws: { function: { url: { invokeMode: "BUFFERED" } } } },
				])
			`,
			"modules/late.transform.ts": `
				import { defineTransform } from "@ocel/transforms"
				export default defineTransform({ aws: { function: { lambda: { memorySize: 512 } } } })
			`,
		}, "./modules/defaults.transform.ts", "./modules/late.transform.ts")
		if err != nil {
			t.Fatalf("Evaluate: %v", err)
		}
		if len(results) != 1 {
			t.Fatalf("results = %d, want 1", len(results))
		}
		lambda := results[0].Patches["lambda"]
		if lambda["memorySize"] != float64(512) {
			t.Errorf("memorySize = %v, want the later module's 512", lambda["memorySize"])
		}
		if lambda["timeout"] != float64(60) {
			t.Errorf("timeout = %v, want the first module's 60", lambda["timeout"])
		}
		if got := results[0].Patches["url"]["invokeMode"]; got != "BUFFERED" {
			t.Errorf("invokeMode = %v, want BUFFERED", got)
		}
	})

	t.Run("a gate that reads false leaves the resource unpatched", func(t *testing.T) {
		t.Parallel()

		results, err := evaluateWith(t, functionRequest(), map[string]string{
			"preview.transform.ts": `
				import { defineTransform } from "@ocel/transforms"
				export default defineTransform({
					if: (ctx) => ctx.envClass === "preview",
					aws: { function: { lambda: { memorySize: 128 } } },
				})
			`,
		}, "./preview.transform.ts")
		if err != nil {
			t.Fatalf("Evaluate: %v", err)
		}
		if len(results[0].Patches) != 0 {
			t.Errorf("patches = %v, want none", results[0].Patches)
		}
	})

	t.Run("a binding output rides through as the placeholder the provider resolves", func(t *testing.T) {
		t.Parallel()

		results, err := evaluateWith(t, functionRequest(), map[string]string{
			"network.transform.ts": `
				import { defineTransform } from "@ocel/transforms"
				export default defineTransform(({ bindings }) => ({
					aws: { function: { lambda: { vpcConfig: { subnetIds: bindings.custom.network.subnetIds } } } },
				}))
			`,
		}, "./network.transform.ts")
		if err != nil {
			t.Fatalf("Evaluate: %v", err)
		}
		vpc, held := results[0].Patches["lambda"]["vpcConfig"].(map[string]any)
		if !held {
			t.Fatalf("lambda patch = %v, want a vpcConfig", results[0].Patches["lambda"])
		}
		placeholder, named := vpc["subnetIds"].(map[string]any)
		if !named {
			t.Fatalf("subnetIds = %v, want a placeholder", vpc["subnetIds"])
		}
		ref, _ := placeholder["$ocelOutput"].(map[string]any)
		if ref["type"] != "custom" || ref["name"] != "network" || ref["property"] != "subnetIds" {
			t.Errorf("placeholder = %v, want bindings.custom.network.subnetIds", ref)
		}
	})

	t.Run("a module with no branch for this provider refuses the deploy", func(t *testing.T) {
		t.Parallel()

		_, err := evaluateWith(t, functionRequest(), map[string]string{
			"gcp.transform.ts": `
				import { defineTransform } from "@ocel/transforms"
				export default defineTransform({ gcp: { service: {} } })
			`,
		}, "./gcp.transform.ts")
		if err == nil {
			t.Fatal("Evaluate() = nil, want a module with no aws branch refused")
		}
		for _, want := range []string{"gcp.transform.ts", "gcp", "aws"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("err = %v, want it to name %q", err, want)
			}
		}
	})

	t.Run("a field ocel fills from what it built refuses the deploy by name", func(t *testing.T) {
		t.Parallel()

		_, err := evaluateWith(t, functionRequest(), map[string]string{
			"role.transform.ts": `
				import { defineTransform } from "@ocel/transforms"
				export default defineTransform({ aws: { function: { lambda: { role: "arn:aws:iam::1:role/mine" } } } })
			`,
		}, "./role.transform.ts")
		if err == nil {
			t.Fatal("Evaluate() = nil, want an owned field refused")
		}
		if !strings.Contains(err.Error(), "aws.function.lambda.role") {
			t.Errorf("err = %v, want it to name the field", err)
		}
	})

	t.Run("a module that exports something else refuses the deploy", func(t *testing.T) {
		t.Parallel()

		_, err := evaluateWith(t, functionRequest(), map[string]string{
			"loose.transform.ts": `export default { aws: {} }`,
		}, "./loose.transform.ts")
		if err == nil {
			t.Fatal("Evaluate() = nil, want a module that skipped defineTransform refused")
		}
		if !strings.Contains(err.Error(), "defineTransform") {
			t.Errorf("err = %v, want it to name defineTransform", err)
		}
	})

	t.Run("tags reach the provider as the union of the rules that carried them", func(t *testing.T) {
		t.Parallel()

		results, err := evaluateWith(t, functionRequest(), map[string]string{
			"tags.transform.ts": `
				import { defineTransform } from "@ocel/transforms"
				export default defineTransform({ tags: { team: "core" }, aws: {} })
			`,
		}, "./tags.transform.ts")
		if err != nil {
			t.Fatalf("Evaluate: %v", err)
		}
		if got := slices.Sorted(maps.Keys(results[0].Tags)); !slices.Equal(got, []string{"team"}) {
			t.Errorf("tags = %v, want team", got)
		}
	})
}
