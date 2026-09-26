package conformance_test

import (
	"testing"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/conformance"
	"github.com/ocelhq/ocel/pkg/provider/resources"
)

func TestTheArtifactTierRunsForAProviderThatKeepsNoStore(t *testing.T) {
	conformance.RunArtifactStore(t, provider.Facts{}, resources.NoArtifacts{})
}
