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
				"vpc":    {"subnetIds": []any{}, "securityGroupIds": []any{}},
			},
		}},
	}
}

func exampleRequest(envClass string) Request {
	return Request{
		EnvClass: envClass,
		Env:      "prod",
		Resources: []Resource{
			{
				Type: "function",
				Name: "api-todos",
				App:  "api",
				Surfaces: Surfaces{
					"lambda": {"memorySizeMb": 1024, "timeoutSeconds": 30, "runtime": "nodejs24.x"},
					"url":    {"invokeMode": "RESPONSE_STREAM"},
					"vpc":    {"subnetIds": []any{}, "securityGroupIds": []any{}},
				},
			},
			{
				Type: "postgres",
				Name: "main",
				App:  "api",
				Surfaces: Surfaces{
					"cluster": {
						"engineVersion":      "16.6",
						"minCapacity":        0,
						"maxCapacity":        4,
						"deletionProtection": false,
						"skipFinalSnapshot":  true,
					},
					"instance": {"instanceClass": "db.serverless", "publiclyAccessible": false},
				},
			},
		},
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

	t.Run("the with-transforms example raises every route and widens production's cluster", func(t *testing.T) {
		t.Parallel()

		root := transformtest.Root(t, map[string]string{
			"infra/defaults.transform.ts": transformtest.ExampleModule(t, "with-transforms", "infra/defaults.transform.ts"),
		})
		pass := NodePass{Root: root, Modules: []string{"./infra/defaults.transform.ts"}}

		results, err := pass.Evaluate(t.Context(), exampleRequest("production"))
		if err != nil {
			t.Fatalf("Evaluate: %v", err)
		}
		if got := results[0].Surfaces["lambda"]["memorySizeMb"]; got != float64(2048) {
			t.Errorf("memorySizeMb = %v, want the example's raised 2048", got)
		}
		if got := results[0].Surfaces["lambda"]["timeoutSeconds"]; got != float64(60) {
			t.Errorf("timeoutSeconds = %v, want the example's raised 60", got)
		}
		if got := results[1].Surfaces["cluster"]["minCapacity"]; got != float64(2) {
			t.Errorf("minCapacity = %v, want production raised to 2", got)
		}
		if got := results[1].Surfaces["cluster"]["maxCapacity"]; got != float64(16) {
			t.Errorf("maxCapacity = %v, want production widened to 16", got)
		}
		if got := results[1].Surfaces["cluster"]["deletionProtection"]; got != true {
			t.Errorf("deletionProtection = %v, want production protected", got)
		}
		for i, result := range results {
			if got := result.Tags["acme:cost-center"]; got != "platform" {
				t.Errorf("resource %d tags = %v, want the org tag on everything", i, result.Tags)
			}
		}

		preview, err := pass.Evaluate(t.Context(), exampleRequest("preview"))
		if err != nil {
			t.Fatalf("Evaluate preview: %v", err)
		}
		if got := preview[1].Surfaces["cluster"]["deletionProtection"]; got != false {
			t.Errorf("deletionProtection = %v, want the production gate closed for a preview", got)
		}
		if got := preview[1].Surfaces["cluster"]["maxCapacity"]; got != float64(4) {
			t.Errorf("maxCapacity = %v, want the provider's own 4 for a preview", got)
		}
	})

	t.Run("the with-sst example places every function in the network its own IaC published", func(t *testing.T) {
		t.Parallel()

		root := transformtest.Root(t, map[string]string{
			"infra/network.transform.ts": transformtest.ExampleModule(t, "with-sst", "infra/network.transform.ts"),
		})
		pass := NodePass{Root: root, Modules: []string{"./infra/network.transform.ts"}}

		results, err := pass.Evaluate(t.Context(), exampleRequest("production"))
		if err != nil {
			t.Fatalf("Evaluate: %v", err)
		}
		for _, field := range []string{"subnetIds", "securityGroupIds"} {
			placeholder, ok := results[0].Surfaces["vpc"][field].(map[string]any)
			if !ok {
				t.Fatalf("vpc.%s = %#v, want a link output the deploy resolves", field, results[0].Surfaces["vpc"][field])
			}
			ref, ok := placeholder["$ocelOutput"].(map[string]any)
			if !ok {
				t.Fatalf("vpc.%s = %#v, want a link output the deploy resolves", field, placeholder)
			}
			if ref["link"] != "network" || ref["property"] != field {
				t.Errorf("vpc.%s reads %v, want network's %s", field, ref, field)
			}
		}
		if _, placed := results[1].Surfaces["vpc"]; placed {
			t.Errorf("the postgres resource carries a vpc surface it has no field for")
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
