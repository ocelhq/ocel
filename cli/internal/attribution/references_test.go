package attribution

import (
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/discovery"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
)

func polyglotProject(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	write(t, filepath.Join(root, "go.mod"), "module example.com/shop\n\ngo 1.27.0\n")
	write(t, filepath.Join(root, "refs", "db.go"), "package refs\n\nvar DB = 1\n")
	write(t, filepath.Join(root, "services", "orders", "main.go"), "package main\n\nimport _ \"example.com/shop/refs\"\n\nfunc main() {}\n")
	write(t, filepath.Join(root, "services", "billing", "main.go"), "package main\n\nfunc main() {}\n")
	write(t, filepath.Join(root, "shared", "db.ts"), "export const db = { name: \"main\" };\n")
	write(t, filepath.Join(root, "apps", "web", "src", "server.ts"), "import { db } from \"../../../shared/db.js\";\n\nexport function handler() {\n  return db.name;\n}\n")
	return root
}

func polyglotApps() []App {
	return []App{
		{Name: "web", Path: "apps/web", Language: discovery.JS},
		{Name: "orders", Path: "services/orders", Language: discovery.Go},
		{Name: "billing", Path: "services/billing", Language: discovery.Go},
	}
}

func TestAReferenceGrantsAResourceDeclaredInAnotherLanguage(t *testing.T) {
	root := polyglotProject(t)
	declarations := []Declaration{{Type: resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES, Name: "main", Source: filepath.Join(root, "shared", "db.ts") + ":1"}}
	references := []Reference{{Type: resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES, Name: "main", Source: filepath.Join(root, "refs", "db.go") + ":3"}}

	t.Run("an app reaching the reference is granted what the declaration provisions", func(t *testing.T) {
		usages, err := Compute(t.Context(), root, polyglotApps(), declarations, references)
		if err != nil {
			t.Fatalf("Compute: %v", err)
		}
		want := []string{
			"orders -> RESOURCE_TYPE_POSTGRES:main [services/orders]",
			"web -> RESOURCE_TYPE_POSTGRES:main [apps/web/src/server.ts]",
		}
		if got := edgeStrings(usages); !reflect.DeepEqual(got, want) {
			t.Errorf("edges = %v, want %v — orders imports the Go reference to the TypeScript declaration, and billing imports neither", got, want)
		}
	})

	t.Run("without the reference the declaration stays inside its own language", func(t *testing.T) {
		usages, err := Compute(t.Context(), root, polyglotApps(), declarations, nil)
		if err != nil {
			t.Fatalf("Compute: %v", err)
		}
		if want := []string{"web -> RESOURCE_TYPE_POSTGRES:main [apps/web/src/server.ts]"}; !reflect.DeepEqual(edgeStrings(usages), want) {
			t.Errorf("edges = %v, want %v", edgeStrings(usages), want)
		}
	})

	t.Run("a reference to another identity grants nothing of this one", func(t *testing.T) {
		other := []Reference{{Type: resourcesv1.ResourceType_RESOURCE_TYPE_BUCKET, Name: "main", Source: references[0].Source}}

		usages, err := Compute(t.Context(), root, polyglotApps(), declarations, other)
		if err != nil {
			t.Fatalf("Compute: %v", err)
		}
		for _, u := range usages {
			if u.App == "orders" && u.Type == resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES {
				t.Errorf("orders was granted the postgres through a reference to a bucket: %v", edgeStrings(usages))
			}
		}
	})

	t.Run("a reference that names no project file fails closed", func(t *testing.T) {
		stray := []Reference{{Type: resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES, Name: "main", Source: filepath.Join(t.TempDir(), "db.go") + ":3"}}

		_, err := Compute(t.Context(), root, polyglotApps(), declarations, stray)

		var unresolved *UnresolvedReferenceError
		if !errors.As(err, &unresolved) {
			t.Fatalf("Compute err = %v, want an *UnresolvedReferenceError", err)
		}
		if !strings.Contains(err.Error(), `"main"`) || !strings.Contains(err.Error(), "db.go:3") {
			t.Errorf("err = %v, want it to name the resource and the reference's source", err)
		}
	})
}
