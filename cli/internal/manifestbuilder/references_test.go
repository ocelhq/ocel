package manifestbuilder

import (
	"errors"
	"strings"
	"testing"

	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
)

func TestBuildReferences(t *testing.T) {
	t.Parallel()

	declarations := []Declaration{
		{Type: resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES, Name: "main", Postgres: &resourcesv1.PostgresConfig{Version: "17"}, Source: "shared/db.ts:3"},
	}

	t.Run("one declaration and references from other files provision one resource", func(t *testing.T) {
		t.Parallel()

		references := []Reference{
			{Type: resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES, Name: "main", Source: "refs/db.go:5"},
			{Type: resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES, Name: "main", Source: "refs/db.py:2"},
		}

		manifest, err := Build("proj-1", nil, []App{
			{Name: "orders", Usages: []Usage{{Type: resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES, Name: "main", Files: []string{"services/orders"}}}},
		}, "serverless", declarations, references, nil, nil, nil)
		if err != nil {
			t.Fatalf("Build err = %v, want a declaration referenced elsewhere to be sharing, not a duplicate", err)
		}
		if got := len(manifest.GetResources()); got != 1 {
			t.Errorf("resources = %v, want exactly the one declared", manifest.GetResources())
		}
		if got := len(manifest.GetUsages()); got != 1 {
			t.Errorf("usages = %v, want the edge the reference grants", manifest.GetUsages())
		}
	})

	t.Run("a reference nothing declares names where it was written", func(t *testing.T) {
		t.Parallel()

		references := []Reference{{Type: resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES, Name: "ghost", Source: "refs/db.go:7"}}

		_, err := Build("proj-1", nil, []App{
			{Name: "orders", Usages: []Usage{{Type: resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES, Name: "ghost", Files: []string{"services/orders"}}}},
		}, "serverless", declarations, references, nil, nil, nil)

		var dangling *DanglingReferenceError
		if !errors.As(err, &dangling) {
			t.Fatalf("Build err = %v, want a *DanglingReferenceError ahead of the usage it would grant", err)
		}
		for _, want := range []string{`"ghost"`, "refs/db.go:7", "postgres"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("err = %v, want it to contain %s", err, want)
			}
		}
	})

	t.Run("a reference to a declared name of another type is still dangling", func(t *testing.T) {
		t.Parallel()

		references := []Reference{{Type: resourcesv1.ResourceType_RESOURCE_TYPE_BUCKET, Name: "main", Source: "refs/db.go:9"}}

		_, err := Build("proj-1", nil, nil, "serverless", declarations, references, nil, nil, nil)

		var dangling *DanglingReferenceError
		if !errors.As(err, &dangling) {
			t.Fatalf("Build err = %v, want a *DanglingReferenceError", err)
		}
		if !strings.Contains(err.Error(), "refs/db.go:9") {
			t.Errorf("err = %v, want it to name the reference's source", err)
		}
	})
}
