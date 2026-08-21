package providerkit_test

import (
	"context"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/providerkit/resources"
	"github.com/ocelhq/ocel/pkg/providerkit/vpswalk"
)

type warm struct{ vpswalk.Provider }

func (warm) Warm(context.Context, []string, providerkit.Reporter) error { return nil }

// A capability must survive a port being composed. Assert against the port and it
// does not: resources.Releaser hands back a fan-out, and the fan-out is what the
// assertion sees.
func TestOptionalSetsAreFoundOnTheRoot(t *testing.T) {
	var provider providerkit.Provider = warm{}

	if _, ok := provider.(providerkit.Warmer); !ok {
		t.Error("Warm is not reachable on the root, so the kit would never call it")
	}

	if _, ok := provider.Releases().(providerkit.Warmer); ok {
		t.Error("Warm is reachable through the port, which only holds while nothing wraps it")
	}
}

func TestFanOutHidesWhatItWraps(t *testing.T) {
	released := resources.Releaser(warm{})

	if _, ok := released.(providerkit.Warmer); ok {
		t.Error("the fan-out forwarded an assertion it cannot forward in general")
	}
}
