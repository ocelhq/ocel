package deploy

import (
	"strings"
	"testing"
)

func TestSliceDatabaseName(t *testing.T) {
	t.Parallel()

	t.Run("joins identity and resource", func(t *testing.T) {
		t.Parallel()

		got := sliceDatabaseName("feature_login_ab12cd34", "postgres_main")
		want := "feature_login_ab12cd34_postgres_main"
		if got != want {
			t.Errorf("sliceDatabaseName() = %q, want %q", got, want)
		}
	})

	t.Run("truncates to a valid postgres identifier", func(t *testing.T) {
		t.Parallel()

		identity := strings.Repeat("a", 50)
		got := sliceDatabaseName(identity, "postgres_main")
		if len(got) > maxPostgresIdentLen {
			t.Errorf("sliceDatabaseName() length = %d, want <= %d", len(got), maxPostgresIdentLen)
		}
	})
}
