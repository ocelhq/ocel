package cloudrun_test

import (
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/edge/edgeconformance"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/cloudrun"
)

func TestTheCloudRunEdgeDeclaresFactsAsEveryEdgeMust(t *testing.T) {
	t.Parallel()

	edgeconformance.RunFacts(t, func(*testing.T) edge.Edge {
		return cloudrun.New("ocel", &pinRecorder{}, &gateRecorder{})
	})
}
