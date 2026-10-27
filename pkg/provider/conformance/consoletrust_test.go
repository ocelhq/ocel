package conformance_test

import (
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/conformance"
	"github.com/ocelhq/ocel/pkg/provider/fake"
)

func TestTheReferenceBootstrapRoundTripsTheConsolesTrust(t *testing.T) {
	bootstrap := fake.NewBootstrap()
	bootstrap.Offers(append(bootstrap.Parts(), provider.Part{Name: provider.ConsoleTrustPart, Summary: "the console's trust"})...)

	conformance.RunConsoleTrust(t, provider.Facts{WebIdentity: &provider.WebIdentityFacts{}}, bootstrap.Parts())
	conformance.RunConsoleTrustBootstrap(t, bootstrap, environment.TierProduction)
}
