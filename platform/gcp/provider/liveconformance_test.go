//go:build integration

package gcp_test

import (
	"context"
	"testing"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/conformance"
	"github.com/ocelhq/ocel/pkg/provider/providerserver"
	gcp "github.com/ocelhq/ocel/platform/gcp/provider"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/alb"
)

func TestLiveGCPProviderConformsWithItsProjectBehindIt(t *testing.T) {
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "conformance")
	conformance.Run(t, conformance.Suite{
		New: func(context.Context, provider.Settings) (provider.Provider, error) {
			return live(t), nil
		},
		Server:       providerserver.Config{Version: "test", New: gcp.New},
		Options:      provider.Options{"project": liveProject(), "region": liveRegion()},
		Binary:       buildProvider(t),
		Certificates: &conformance.CertificateChecks{Kind: alb.Kind},
	})
}
