package cloudflare

import (
	"testing"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const defaultNamespace = "ocel"

func TestConventionWorkerNames(t *testing.T) {
	t.Parallel()

	t.Run("production names every worker family the project deploys", func(t *testing.T) {
		t.Parallel()

		got, err := conventionWorkerNames(defaultNamespace, "shop", edge.ClassProduction, []string{"web"})
		if err != nil {
			t.Fatalf("conventionWorkerNames: %v", err)
		}
		assertSet(t, "names", got, []string{
			"ocel--shop--prod--root",
			"ocel--shop--prod--web",
		})
	})

	t.Run("preview names the worker the preview class deploys", func(t *testing.T) {
		t.Parallel()

		got, err := conventionWorkerNames(defaultNamespace, "shop", edge.ClassPreview, nil)
		if err != nil {
			t.Fatalf("conventionWorkerNames: %v", err)
		}
		assertSet(t, "names", got, []string{"ocel--shop--preview--root"})
	})

	t.Run("another namespace deploying the same slug derives its own names", func(t *testing.T) {
		t.Parallel()

		mine, err := conventionWorkerNames(defaultNamespace, "shop", edge.ClassProduction, nil)
		if err != nil {
			t.Fatalf("conventionWorkerNames: %v", err)
		}
		theirs, err := conventionWorkerNames("j-1874-deploy-next-cloudflare", "shop", edge.ClassProduction, nil)
		if err != nil {
			t.Fatalf("conventionWorkerNames: %v", err)
		}
		for _, name := range theirs {
			for _, held := range mine {
				if name == held {
					t.Errorf("a sweep of this namespace names %q, which is the worker another namespace deploys for the same slug", name)
				}
			}
		}
	})

	t.Run("a state that names no namespace, class or slug derives nothing", func(t *testing.T) {
		t.Parallel()

		for _, tc := range []struct {
			namespace string
			slug      string
			class     edge.Class
		}{
			{namespace: defaultNamespace, slug: "", class: edge.ClassProduction},
			{namespace: defaultNamespace, slug: "shop", class: ""},
			{namespace: "", slug: "shop", class: edge.ClassProduction},
		} {
			got, err := conventionWorkerNames(tc.namespace, tc.slug, tc.class, []string{"web"})
			if err != nil {
				t.Fatalf("conventionWorkerNames(%q, %q, %q): %v", tc.namespace, tc.slug, tc.class, err)
			}
			if len(got) != 0 {
				t.Errorf("names = %v, want none: nothing identifies the project's workers", got)
			}
		}
	})

	t.Run("an unknown class is an error", func(t *testing.T) {
		t.Parallel()

		if _, err := conventionWorkerNames(defaultNamespace, "shop", edge.Class("nonsense"), nil); err == nil {
			t.Error("conventionWorkerNames(unknown class) err = nil, want an error")
		}
	})
}

func TestProjectOwnsScriptReadsOnlyThisNamespacesWorkers(t *testing.T) {
	t.Parallel()

	const other = "j-1874-deploy-next-cloudflare"
	theirs, err := conventionWorkerNames(other, "shop", edge.ClassProduction, nil)
	if err != nil {
		t.Fatalf("conventionWorkerNames: %v", err)
	}
	owns := projectOwnsScript(defaultNamespace, "shop")
	for _, name := range theirs {
		if owns(name) {
			t.Errorf("%q reads as this namespace's, so a prune here would take a route off the worker %s deploys", name, other)
		}
	}
	if !projectOwnsScript(other, "shop")(theirs[0]) {
		t.Errorf("%q does not read as %s's own worker", theirs[0], other)
	}
}
