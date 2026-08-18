package cloudflare

import (
	"testing"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func TestConventionWorkerNames(t *testing.T) {
	t.Parallel()

	t.Run("production names every family the project ever deployed under", func(t *testing.T) {
		t.Parallel()

		got, err := conventionWorkerNames("shop", edge.ClassProduction, []string{"web"})
		if err != nil {
			t.Fatalf("conventionWorkerNames: %v", err)
		}
		assertSet(t, "names", got, []string{
			"ocel-shop-prod",
			"ocel-shop--prod-root",
			"ocel--shop--prod--root",
			"ocel-shop--prod-web",
			"ocel--shop--prod--web",
		})
	})

	t.Run("preview names the worker the preview class deploys", func(t *testing.T) {
		t.Parallel()

		got, err := conventionWorkerNames("shop", edge.ClassPreview, nil)
		if err != nil {
			t.Fatalf("conventionWorkerNames: %v", err)
		}
		assertSet(t, "names", got, []string{
			"ocel-shop-preview",
			"ocel-shop--preview-root",
			"ocel--shop--preview--root",
		})
	})

	t.Run("a state that names no class or slug derives nothing", func(t *testing.T) {
		t.Parallel()

		for _, tc := range []struct {
			slug  string
			class edge.Class
		}{
			{slug: "", class: edge.ClassProduction},
			{slug: "shop", class: ""},
		} {
			got, err := conventionWorkerNames(tc.slug, tc.class, []string{"web"})
			if err != nil {
				t.Fatalf("conventionWorkerNames(%q, %q): %v", tc.slug, tc.class, err)
			}
			if len(got) != 0 {
				t.Errorf("names = %v, want none: nothing identifies the project's workers", got)
			}
		}
	})

	t.Run("an unknown class is an error", func(t *testing.T) {
		t.Parallel()

		if _, err := conventionWorkerNames("shop", edge.Class("nonsense"), nil); err == nil {
			t.Error("conventionWorkerNames(unknown class) err = nil, want an error")
		}
	})
}
