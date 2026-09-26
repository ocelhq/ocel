package conformance_test

import (
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/providerkit/conformance"
	"github.com/ocelhq/ocel/pkg/providerkit/provider"
)

func TestTheArtifactTierRunsForAProviderThatKeepsNoStore(t *testing.T) {
	conformance.RunArtifactStore(t, provider.Facts{}, providerkit.NoArtifacts{})
}
