package naming_test

import (
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/naming"
)

func TestARouteTableKeySitsInItsReleasePrefixNamedByItsDigest(t *testing.T) {
	coordinate := naming.Coordinate{Project: "shop", Env: "production", App: "web", Release: naming.NewReleaseToken("dep1", "fp1")}
	digest := strings.Repeat("ab", 32)
	want := coordinate.StoragePrefix() + "route-table/" + digest + ".json"
	if got := coordinate.RouteTableKey(digest); got != want {
		t.Errorf("RouteTableKey = %q, want %q", got, want)
	}
}
