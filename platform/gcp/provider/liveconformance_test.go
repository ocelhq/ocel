//go:build integration

package gcp_test

import (
	"context"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/conformance"
	"github.com/ocelhq/ocel/pkg/provider/providerserver"
	gcp "github.com/ocelhq/ocel/platform/gcp/provider"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/albresources"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/bootstrap"
)

type servedHere struct{ provider.Provider }

func (s servedHere) Facts() provider.Facts { return servableFacts(s.Provider.Facts()) }

func TestLiveGCPProviderConformsWithItsProjectBehindIt(t *testing.T) {
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "conformance")
	if emulated() {
		t.Setenv(provider.NamespaceEnvVar, namesOf(t, live(t)).Namespace().String()+"-conformance")
	}
	bootstrappedWith(t, live(t), environment.TierProduction, bootstrap.EdgeBucketPart, bootstrap.CacheBucketPart)
	projectDatabaseMade(t, live(t), environment.TierPreview, "conformance")

	conformance.Run(t, conformance.Suite{
		New: func(context.Context, provider.Settings) (provider.Provider, error) {
			return servedHere{live(t)}, nil
		},
		Server:       providerserver.Config{Version: "test", New: gcp.New},
		Options:      provider.Options{"project": liveProject(), "region": liveRegion()},
		Binary:       buildProvider(t),
		Certificates: &conformance.CertificateChecks{Kind: albresources.Kind},
	})
}
