package deploy

import (
	"testing"

	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/resources/v1"
)

func TestTranslatePostgres(t *testing.T) {
	t.Parallel()

	t.Run("fixed serverless v2 defaults", func(t *testing.T) {
		t.Parallel()

		got := translatePostgres(&resourcesv1.PostgresConfig{Version: "15"})

		if got.Engine != "aurora-postgresql" {
			t.Errorf("Engine = %q, want aurora-postgresql", got.Engine)
		}
		if got.EngineMode != "provisioned" {
			t.Errorf("EngineMode = %q, want provisioned (serverless v2 runs provisioned + scaling config)", got.EngineMode)
		}
		if got.MinCapacity != 0 {
			t.Errorf("MinCapacity = %v, want 0 (scale to zero)", got.MinCapacity)
		}
		if got.MaxCapacity != 2 {
			t.Errorf("MaxCapacity = %v, want 2", got.MaxCapacity)
		}
		if got.InstanceClass != "db.serverless" {
			t.Errorf("InstanceClass = %q, want db.serverless", got.InstanceClass)
		}
		if !got.ManageMasterPassword {
			t.Error("ManageMasterPassword = false, want true (RDS-managed secret)")
		}
		if got.PubliclyAccessible {
			t.Error("PubliclyAccessible = true, want false (private)")
		}
		if got.DeletionProtection {
			t.Error("DeletionProtection = true, want false (clean teardown)")
		}
		if !got.SkipFinalSnapshot {
			t.Error("SkipFinalSnapshot = false, want true (clean teardown)")
		}
		if got.Port != 5432 {
			t.Errorf("Port = %d, want 5432", got.Port)
		}
	})

	cases := []struct {
		name    string
		version string
		want    string
	}{
		{"uses the configured version", "15", "15"},
		{"an empty version falls back to the pinned default", "", defaultPostgresEngineVersion},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := translatePostgres(&resourcesv1.PostgresConfig{Version: tc.version})
			if got.EngineVersion != tc.want {
				t.Errorf("EngineVersion = %q, want %q", got.EngineVersion, tc.want)
			}
		})
	}
}
