//go:build integration

package vps_test

import (
	"context"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/conformance"
	"github.com/ocelhq/ocel/pkg/provider/providerserver"
	vps "github.com/ocelhq/ocel/platform/vps/provider"
	"github.com/ocelhq/ocel/platform/vps/provider/certs"
)

func TestLiveVPSProviderConformsWithItsBoxBehindIt(t *testing.T) {
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "conformance")
	vm := liveMachine(t)
	vm.purges(t)

	bootstrapping := vm.provider(t)
	defer closing(t, bootstrapping)
	bootstrap, err := bootstrapping.Bootstrap("")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := bootstrap.Apply(ctx, provider.BootstrapRequest{Tier: environment.TierProduction, WrittenBy: "live-suite"}, progress.Discard()); err != nil {
		t.Fatalf("Apply(%s) = %v, want the record tier every port beneath it writes into", environment.TierProduction, err)
	}
	for _, tier := range []environment.Tier{environment.TierProduction, environment.TierPreview} {
		defer func() {
			if err := bootstrap.Remove(ctx, tier, progress.Discard()); err != nil {
				t.Errorf("Remove(%s) = %v", tier, err)
			}
		}()
	}

	conformance.Run(t, conformance.Suite{
		New: func(context.Context, provider.Settings) (provider.Provider, error) {
			p := vm.provider(t)
			t.Cleanup(func() { closing(t, p) })
			return p, nil
		},
		Server:  providerserver.Config{Version: "test", New: vps.NewReadingSSHConfig(vm.config)},
		Options: provider.Options{"ssh": map[string]any{"host": vm.addr, "user": vm.user, "identityFile": vm.key}},
		Binary:  buildProvider(t),
		Certificates: &conformance.CertificateChecks{
			Kind:      edge.None,
			Hostnames: []string{"shop.example.com", "www.shop.example.com"},
			Handle:    certs.ProxyHandle,
		},
	})
}
