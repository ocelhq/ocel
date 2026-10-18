//go:build integration

package vps_test

import (
	"context"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/conformance"
	"github.com/ocelhq/ocel/pkg/provider/providerserver"
	vps "github.com/ocelhq/ocel/platform/vps/provider"
	"github.com/ocelhq/ocel/platform/vps/provider/certs"
)

func TestLiveVPSProviderConformsWithItsBoxBehindIt(t *testing.T) {
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "conformance")
	vm := liveMachine(t)
	conformance.Run(t, conformance.Suite{
		New: func(context.Context, provider.Settings) (provider.Provider, error) {
			p := vm.provider(t)
			t.Cleanup(func() { closing(t, p) })
			return p, nil
		},
		Server:  providerserver.Config{Version: "test", New: vps.New},
		Options: provider.Options{"ssh": map[string]any{"host": "203.0.113.10"}},
		Binary:  buildProvider(t),
		Certificates: &conformance.CertificateChecks{
			Kind:      edge.None,
			Hostnames: []string{"shop.example.com", "www.shop.example.com"},
			Handle:    certs.ProxyHandle,
		},
	})
}
