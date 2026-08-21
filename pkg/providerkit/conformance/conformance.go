package conformance

import (
	"context"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

type Suite struct {
	New func(ctx context.Context, options providerkit.Options) (providerkit.Provider, error)

	Spec providerkit.Spec

	Options providerkit.Options

	Binary string
}

func Run(t *testing.T, suite Suite) {
	t.Helper()

	t.Run("optional sets", func(t *testing.T) { runOptionalSets(t, suite) })
	t.Run("wire", func(t *testing.T) { runWire(t, suite) })
}
