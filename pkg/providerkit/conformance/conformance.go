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

	Vendor func(t *testing.T, provider providerkit.Provider)

	Certifier *CertifierChecks
}

func Run(t *testing.T, suite Suite) {
	t.Helper()

	t.Run("ports", func(t *testing.T) { runPorts(t, suite) })
	t.Run("optional sets", func(t *testing.T) { runOptionalSets(t, suite) })
	t.Run("wire", func(t *testing.T) { runWire(t, suite) })
	t.Run("certifier", func(t *testing.T) { runCertifier(t, suite) })
	t.Run("vendor", func(t *testing.T) { runVendor(t, suite) })
}

func runVendor(t *testing.T, suite Suite) {
	t.Helper()

	if suite.Vendor == nil {
		t.Skip("this provider hangs no checks of its own here; its live tests are its own to run")
	}
	if suite.New == nil {
		t.Fatal("the suite carries vendor checks and no constructor, so there is no provider to run them against")
	}
	provider, err := suite.New(context.Background(), suite.Options)
	if err != nil {
		t.Fatalf("New() error = %v, want a provider", err)
	}
	suite.Vendor(t, provider)
}
