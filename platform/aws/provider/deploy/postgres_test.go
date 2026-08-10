package deploy

import (
	"testing"

	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/resources/v1"
)

func TestTranslatePostgres_FixedServerlessV2Defaults(t *testing.T) {
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
}

func TestTranslatePostgres_UsesConfiguredVersion(t *testing.T) {
	got := translatePostgres(&resourcesv1.PostgresConfig{Version: "15"})
	if got.EngineVersion != "15" {
		t.Errorf("EngineVersion = %q, want the configured 15", got.EngineVersion)
	}
}

func TestTranslatePostgres_EmptyVersionFallsBackToPinnedDefault(t *testing.T) {
	got := translatePostgres(&resourcesv1.PostgresConfig{})
	if got.EngineVersion != defaultPostgresEngineVersion {
		t.Errorf("EngineVersion = %q, want the pinned default %q", got.EngineVersion, defaultPostgresEngineVersion)
	}
}
