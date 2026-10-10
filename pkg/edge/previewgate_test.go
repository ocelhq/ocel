package edge_test

import (
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/edge/edgeconformance"
)

func TestThePreviewGateAnswersEveryConformanceVector(t *testing.T) {
	t.Parallel()

	edgeconformance.RunPreviewGate(t)
}

func TestAPreviewPasswordHashesToTheValueTheVectorsStore(t *testing.T) {
	t.Parallel()

	const want = "f18e0cd484c475051052b9fed4f125e1db487c694fc74b1128b40bb4af41e5a3"
	if got := edge.HashPreviewPassword("test-preview-key", "JBSWY3DPEHPK3PXP2345"); got != want {
		t.Errorf("HashPreviewPassword = %q, want %q", got, want)
	}
}
