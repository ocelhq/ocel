package host

import (
	"testing"
)

func TestTheStoresAdminApiIsRefusedAtTheEdgeAheadOfItsForward(t *testing.T) {
	t.Parallel()

	answer := routedBy(t, storing())
	for _, path := range []string{"/rustfs", "/rustfs/console", "/health", "/health/ready", "/RUSTFS/x", "/bucket/../rustfs/x"} {
		if upstream, ok := answer("storage.shop.example.com", path); ok {
			t.Errorf("%s on the store's hostname reaches %s, and that is its admin api and its liveness on the open internet", path, upstream)
		}
	}
	if _, ok := answer("storage.shop.example.com", "/bucket/key"); !ok {
		t.Error("an object path on the store's hostname is refused along with its admin api")
	}
	if _, ok := answer("shop.example.com", "/health"); !ok {
		t.Error("an app's own /health is refused as if it were the store's")
	}
}
