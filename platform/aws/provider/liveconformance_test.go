//go:build integration

package aws_test

import (
	"testing"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/conformance"
	"github.com/ocelhq/ocel/pkg/provider/providerserver"
	aws "github.com/ocelhq/ocel/platform/aws/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/edges"
)

func TestLiveAWSProviderConformsWithItsAccountBehindIt(t *testing.T) {
	live(t)
	conformance.Run(t, conformance.Suite{
		New:          aws.New,
		Server:       providerserver.Config{Version: "test", New: aws.New},
		Options:      provider.Options{"region": liveRegion},
		Binary:       buildProvider(t),
		Certificates: &conformance.CertificateChecks{Kind: edges.DefaultKind},
	})
}
