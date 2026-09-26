package conformance

import (
	"context"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/providerkit/provider"
)

type Suite struct {
	New func(ctx context.Context, settings provider.Settings) (provider.Provider, error)

	Spec providerkit.Spec

	Options provider.Options

	Binary string

	Vendor func(t *testing.T, p provider.Provider)

	Certificates *CertificateChecks
}

func Run(t *testing.T, suite Suite) {
	t.Helper()

	t.Run("ports", func(t *testing.T) { runPorts(t, suite) })
	t.Run("wire", func(t *testing.T) { runWire(t, suite) })
	t.Run("certificates", func(t *testing.T) { runCertificates(t, suite) })
	t.Run("hooks", func(t *testing.T) { runHooks(t, suite) })
	t.Run("vendor", func(t *testing.T) { runVendor(t, suite) })
}

func runHooks(t *testing.T, suite Suite) {
	t.Helper()

	if suite.New == nil {
		t.Skip("the suite carries no constructor, so there are no hooks to read")
	}
	p, err := suite.New(context.Background(), provider.Settings{Options: suite.Options})
	if err != nil {
		t.Fatalf("New() error = %v, want a provider", err)
	}
	hooks := p.Hooks()

	t.Run("Cost", func(t *testing.T) {
		if hooks.Cost == nil {
			t.Skip("this provider sets no Cost hooks, so no deploy of it is priced")
		}
		runCost(t, suite)
	})
}

func runVendor(t *testing.T, suite Suite) {
	t.Helper()

	if suite.Vendor == nil {
		t.Skip("this provider hangs no checks of its own here; its live tests are its own to run")
	}
	if suite.New == nil {
		t.Fatal("the suite carries vendor checks and no constructor, so there is no provider to run them against")
	}
	p, err := suite.New(context.Background(), provider.Settings{Options: suite.Options})
	if err != nil {
		t.Fatalf("New() error = %v, want a provider", err)
	}
	suite.Vendor(t, p)
}
