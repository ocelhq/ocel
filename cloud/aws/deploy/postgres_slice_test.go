package deploy

import (
	"strings"
	"testing"
)

func TestSliceDatabaseName_JoinsIdentityAndResource(t *testing.T) {
	got := sliceDatabaseName("feature_login_ab12cd34", "postgres_main")
	want := "feature_login_ab12cd34_postgres_main"
	if got != want {
		t.Errorf("sliceDatabaseName() = %q, want %q", got, want)
	}
}

func TestSliceDatabaseName_TruncatesToValidPostgresIdentifier(t *testing.T) {
	identity := strings.Repeat("a", 50)
	got := sliceDatabaseName(identity, "postgres_main")
	if len(got) > maxPostgresIdentLen {
		t.Errorf("sliceDatabaseName() length = %d, want <= %d", len(got), maxPostgresIdentLen)
	}
}
