package manifest

import (
	"fmt"
	"path/filepath"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/declaration"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
)

func TestADeclaredResourceCarriesItsFieldsAndWhereItWasDeclared(t *testing.T) {
	t.Parallel()

	t.Run("maps resource fields", func(t *testing.T) {
		t.Parallel()

		resources := []declaration.Resource{
			{
				Name:     "main",
				Type:     resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES,
				Postgres: &resourcesv1.PostgresConfig{Version: "17"},
			},
		}

		decls := declaredResources(t.TempDir(), resources)

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
		resources := []declaration.Resource{{
			Name:   "main",
			Type:   resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES,
			Source: filepath.Join(configDir, "shared", "db.ts") + ":3",
		}}

		decls := declaredResources(configDir, resources)

		if len(decls) != 1 {
			t.Fatalf("len(decls) = %d, want 1", len(decls))
		}
		if decls[0].Source != "shared/db.ts:3" {
			t.Errorf("Source = %q, want %q", decls[0].Source, "shared/db.ts:3")
		}
	})
}

func TestANameThatIsNotLowercaseWordsJoinedByHyphensIsRefused(t *testing.T) {
	t.Parallel()

	for name, declared := range map[string]declaredResource{
		"topic":    topic("Orders", "src/orders.ts:1", nil),
		"task":     task("-resize", "src/resize.ts:1", nil),
		"worker":   worker("media--jobs", "src/media.ts:1", nil),
		"consumer": consumer("email_x", "src/email.ts:1", &resourcesv1.ConsumerConfig{Topic: "orders"}),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			declarations := []declaredResource{topic("orders", "src/o.ts:1", nil), declared}
			if declared.Type == resourcesv1.ResourceType_RESOURCE_TYPE_TOPIC {
				declarations = []declaredResource{declared}
			}
			_, err := assembleWorkers([]app{{Name: "web"}}, declarations, nil)
			refusedAt(t, err, declared.Source, fmt.Sprintf("%q", declared.Name))
		})
	}
}
