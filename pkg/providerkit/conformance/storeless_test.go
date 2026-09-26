package conformance_test

import (
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit/conformance"
	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	"github.com/ocelhq/ocel/pkg/providerkit/resources"
)

func TestTheArtifactTierRunsForAProviderThatKeepsNoStore(t *testing.T) {
	conformance.RunArtifactStore(t, provider.Facts{}, resources.NoArtifacts{})
}
