package alb

import (
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
)

func TestAProjectsSurfaceNamesItsNamespace(t *testing.T) {
	t.Parallel()

	if got, want := Surface("ocel-dev", "shop", environment.TierProduction), "ocel-dev--alb--shop--prod"; got != want {
		t.Errorf("Surface() = %q, want %q", got, want)
	}
}
