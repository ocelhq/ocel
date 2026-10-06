package cloudflare

import (
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/cloudflare/cloudflare-go/v4/r2"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/edge/edgeconformance"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/router"
	"github.com/ocelhq/ocel/pkg/router/routerconformance"
)

func TestTheCloudflareRouterBehavesAsEveryRouterMust(t *testing.T) {
	routerconformance.Run(t, routerconformance.Suite{
		New:         cloudflareRouterFixture,
		Previews:    cloudflareRouterFixture,
		Hostname:    "shop.app.com",
		PreviewBase: "preview.app.com",
	})
}

func cloudflareRouterFixture(t *testing.T) routerconformance.Fixture {
	t.Helper()
	t.Setenv(envAccountID, "acct")
	store, served := fakeStoreFor(t, "")
	var failing atomic.Pointer[error]
	handler := store.Config.Handler
	store.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/move-pointer") {
			if failure := failing.Swap(nil); failure != nil {
				http.Error(w, (*failure).Error(), http.StatusUnprocessableEntity)
				return
			}
		}
		handler.ServeHTTP(w, r)
	})
	p := previewZoneMock().provider(t)
	spec := previewSpec(store.URL, "v1")
	reconciled, err := p.Reconcile(t.Context(), spec, edge.StackState{})
	if err != nil {
		t.Fatalf("Reconcile the edge: %v", err)
	}
	state := reconciled.State()
	return routerconformance.Fixture{
		Router: Router{p: p},
		Spec:   router.StackSpec{Tier: state.Tier, Slug: state.Slug},
		Prior:  router.NewStackState(state),
		Serving: func(pointer string) string {
			return served.serving(pointer, routerconformance.App)
		},
		FailNextPointerMove: func(err error) { failing.Store(&err) },
	}
}

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

func TestTheCloudflareEdgeForAMutualTLSOriginBehavesAsEveryEdgeMust(t *testing.T) {
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
			p.workerClientCertificate = true
			p.objects = func(string, r2.TemporaryCredentialNewResponse) objectAPI { return &fakeObjects{} }
			return p, environment.TierProduction
		},
	})
}
