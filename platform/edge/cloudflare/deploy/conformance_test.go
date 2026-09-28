package cloudflare

import (
	"testing"

	"github.com/cloudflare/cloudflare-go/v4/r2"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/edge/edgeconformance"
	"github.com/ocelhq/ocel/pkg/environment"
)

func TestTheCloudflareEdgeBehavesAsEveryEdgeMust(t *testing.T) {
	edgeconformance.Run(t, edgeconformance.Suite{
		New: func(t *testing.T) (edge.Edge, edge.StackSpec) {
			t.Setenv(envAccountID, "acct")
			store := fakeStoreServer(t, "")
			return previewZoneMock().provider(t), previewSpec(store.URL, "v1")
		},
		Hostname: "shop.app.com",
		Bootstrap: func(t *testing.T) (edge.Edge, environment.Tier) {
			seedBootstrapBundles(t, "export default {}", "export default {writer:1}")
			p := bootstrapMock(t, false).provider(t)
			p.objects = func(string, r2.TemporaryCredentialNewResponse) objectAPI { return &fakeObjects{} }
			return p, environment.TierProduction
		},
	})
}
