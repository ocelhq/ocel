package manifestwire

import (
	"path/filepath"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/declare"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
)

func TestDeclarations(t *testing.T) {
	t.Parallel()

	t.Run("maps resource fields", func(t *testing.T) {
		t.Parallel()

		resources := []declare.Resource{
			{
				Name:     "main",
				Type:     resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES,
				Postgres: &resourcesv1.PostgresConfig{Version: "17"},
			},
		}

		decls := Declarations(t.TempDir(), resources)

		if len(decls) != 1 {
			t.Fatalf("len(decls) = %d, want 1", len(decls))
		}
		d := decls[0]
		if d.Name != "main" {
			t.Errorf("Name = %q, want %q", d.Name, "main")
		}
		if d.Type != resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES {
			t.Errorf("Type = %v, want %v", d.Type, resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES)
		}
		if d.Postgres.GetVersion() != "17" {
			t.Errorf("Postgres.Version = %q, want %q", d.Postgres.GetVersion(), "17")
		}
	})

	t.Run("reads the declaring file out of the reported source", func(t *testing.T) {
		t.Parallel()

		configDir := t.TempDir()
		resources := []declare.Resource{{
			Name:   "main",
			Type:   resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES,
			Source: filepath.Join(configDir, "shared", "db.ts") + ":3",
		}}

		decls := Declarations(configDir, resources)

		if len(decls) != 1 {
			t.Fatalf("len(decls) = %d, want 1", len(decls))
		}
		if decls[0].Source != "shared/db.ts:3" {
			t.Errorf("Source = %q, want %q", decls[0].Source, "shared/db.ts:3")
		}
	})
}
