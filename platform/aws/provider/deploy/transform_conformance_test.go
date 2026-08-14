package deploy

import (
	"maps"
	"slices"
	"testing"

	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/resources/v1"
	"github.com/ocelhq/ocel/platform/aws/provider/transform"
	"github.com/ocelhq/ocel/platform/aws/provider/transform/transformtest"
)

const conformanceModule = `
	import { defineTransform } from "@ocel/provider-aws/transform"
	const identity = (args) => args
	export default defineTransform({
		function: { lambda: identity, url: identity },
		bucket: { bucket: identity, cors: identity, listener: identity, notification: identity },
		postgres: { cluster: identity, instance: identity },
	})
`

func TestSurfaceConformance(t *testing.T) {
	t.Parallel()

	targeted := map[string][]string{
		"function": {"lambda", "url"},
		"bucket":   {"bucket", "cors", "listener", "notification"},
		"postgres": {"cluster", "instance"},
	}

	rendered := map[string]transform.Surfaces{
		"function": functionSurfaces(translateFunction(&deploymentsv1.ManifestFunction{})),
		"bucket":   bucketSurfaces(translateBucket(&resourcesv1.BucketConfig{})),
		"postgres": postgresSurfaces(translatePostgres(&resourcesv1.PostgresConfig{})),
	}

	t.Run("the provider renders exactly the underlying resources the module targets", func(t *testing.T) {
		t.Parallel()

		for kind, want := range targeted {
			got := slices.Sorted(maps.Keys(rendered[kind]))
			slices.Sort(want)
			if !slices.Equal(got, want) {
				t.Errorf("%s renders %v, want %v", kind, got, want)
			}
		}
	})

	t.Run("every rendered field is one the authored surface allows, and none is missing", func(t *testing.T) {
		t.Parallel()

		root := transformtest.Root(t, map[string]string{
			"conformance.transform.ts": conformanceModule,
		})

		req := transform.Request{EnvClass: "production", Env: "prod"}
		for kind, surfaces := range rendered {
			req.Resources = append(req.Resources, transform.Resource{
				Type: kind, Name: kind + "-under-test", Surfaces: surfaces,
			})
		}
		slices.SortFunc(req.Resources, func(a, b transform.Resource) int {
			if a.Name < b.Name {
				return -1
			}
			return 1
		})

		if _, err := (transform.NodePass{
			Root:    root,
			Modules: []string{"./conformance.transform.ts"},
		}).Evaluate(t.Context(), req); err != nil {
			t.Fatalf("the Go surfaces and the authored allowlist disagree: %v", err)
		}
	})
}
