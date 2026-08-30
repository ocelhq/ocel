package conformance_test

import (
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/providerkit/conformance"
)

func TestTheArtifactTierRunsForAProviderThatKeepsNoStore(t *testing.T) {
	conformance.RunArtifactStore(t, providerkit.NoArtifacts{})
}
