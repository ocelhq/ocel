package cloudflare

import (
	"testing"

	"github.com/cloudflare/cloudflare-go/v4/r2"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
	"github.com/ocelhq/ocel/platform/edge/contract/edgeconformance"
)

func TestCloudflareEdgeConformance(t *testing.T) {
	edgeconformance.Run(t, edgeconformance.Suite{
		New: func(t *testing.T) (edge.Edge, edge.StackSpec) {
			t.Setenv(envAccountID, "acct")
			store := fakeStoreServer(t, "")
			return previewZoneMock().provider(t), previewSpec(store.URL, "v1")
		},
		Hostname: "shop.app.com",
		Bootstrap: func(t *testing.T) (edge.Edge, edge.Class) {
			seedBootstrapBundles(t, "export default {}", "export default {writer:1}")
			p := bootstrapMock(t, false).provider(t)
			p.objects = func(string, r2.TemporaryCredentialNewResponse) objectAPI { return &fakeObjects{} }
			return p, edge.ClassProduction
		},
	})
}
