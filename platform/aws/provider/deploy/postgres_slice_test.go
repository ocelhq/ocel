package deploy

import (
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/naming"
)

func sliceCoordinate(project, app, name string) naming.Coordinate {
	return naming.Coordinate{
		Project: project,
		Env:     "prod",
		App:     app,
		Kind:    naming.KindDatabase,
		Name:    name,
		Release: naming.NewRelease("b7f3a91c", "fp8a1c"),
	}
}

func TestSliceDatabaseName(t *testing.T) {
	t.Parallel()

	t.Run("renders the whole coordinate in underscores", func(t *testing.T) {
		t.Parallel()

		got := sliceDatabaseName(sliceCoordinate("shop", "web", "main"))
		release := naming.NewRelease("b7f3a91c", "fp8a1c").String()
		want := "shop_prod_web_main_" + release
		if got != want {
			t.Errorf("sliceDatabaseName() = %q, want %q", got, want)
		}
		if strings.Contains(got, naming.WordSeparator) {
			t.Errorf("sliceDatabaseName() = %q, want no %q — Postgres identifiers take underscores", got, naming.WordSeparator)
		}
	})

	t.Run("stays a valid postgres identifier", func(t *testing.T) {
		t.Parallel()

		got := sliceDatabaseName(sliceCoordinate("shop", strings.Repeat("a", 40), strings.Repeat("b", 40)))
		if len(got) > maxPostgresIdentLen {
			t.Errorf("sliceDatabaseName() length = %d, want <= %d", len(got), maxPostgresIdentLen)
		}
		if first := got[0]; !(first >= 'a' && first <= 'z') {
			t.Errorf("sliceDatabaseName() = %q, want a letter first", got)
		}
	})

	t.Run("a leading digit in the project cannot start an identifier", func(t *testing.T) {
		t.Parallel()

		got := sliceDatabaseName(sliceCoordinate("7shop", "web", "main"))
		if first := got[0]; !(first >= 'a' && first <= 'z') {
			t.Errorf("sliceDatabaseName() = %q, want a letter first", got)
		}
	})

	t.Run("two long names sharing a prefix stay distinct", func(t *testing.T) {
		t.Parallel()

		shared := strings.Repeat("reporting-", 6)
		a := sliceDatabaseName(sliceCoordinate("shop", "web", shared+"alpha"))
		b := sliceDatabaseName(sliceCoordinate("shop", "web", shared+"beta"))
		if a == b {
			t.Errorf("sliceDatabaseName() collided on %q and %q: both %q", shared+"alpha", shared+"beta", a)
		}
	})
}
