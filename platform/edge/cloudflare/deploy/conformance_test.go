package cloudflare

import (
	"testing"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
	"github.com/ocelhq/ocel/platform/edge/contract/edgeconformance"
)

func TestCloudflareEdgeConformance(t *testing.T) {
	edgeconformance.Run(t, edgeconformance.Suite{
		New: func(t *testing.T) (edge.Edge, edge.StackSpec) {
			t.Setenv(envAccountID, "acct")
			store := fakeStoreServer(t, "s3cr3t")
			return previewZoneMock().provider(t), previewSpec(store.URL, "v1")
		},
	})
}
