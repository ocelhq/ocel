package naming_test

import (
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/naming"
)

func TestTheISRPrefixUnderAReleasePrefixIsItsIsrFolder(t *testing.T) {
	got, ok := naming.ISRPrefixUnder("production/shop/web/r1a2b3c4d/")
	if !ok || got != "production/shop/web/r1a2b3c4d/isr" {
		t.Errorf("ISRPrefixUnder(release prefix) = %q, %v, want its isr folder without a trailing slash", got, ok)
	}
}

func TestARouteTableKeySitsInItsReleasePrefixNamedByItsDigest(t *testing.T) {
	coordinate := naming.Coordinate{Project: "shop", Env: "production", App: "web", Release: naming.NewReleaseToken("dep1", "fp1")}
	digest := strings.Repeat("ab", 32)
	want := coordinate.StoragePrefix() + "route-table/" + digest + ".json"
	if got := coordinate.RouteTableKey(digest); got != want {
		t.Errorf("RouteTableKey = %q, want %q", got, want)
	}
}

func TestAnISRPrefixIsTheISRPrefixUnderItself(t *testing.T) {
	got, ok := naming.ISRPrefixUnder("production/shop/web/r1a2b3c4d/isr/")
	if !ok || got != "production/shop/web/r1a2b3c4d/isr" {
		t.Errorf("ISRPrefixUnder(isr prefix) = %q, %v, want itself without a trailing slash", got, ok)
	}
}

func TestAProjectOrUnclosedPrefixCoversNoISRPrefix(t *testing.T) {
	for _, prefix := range []string{
		"",
		"production/",
		"production/shop/",
		"production/shop/web/",
		"production/shop/web/r1a2b3c4d",
		"production/shop/web/r1a2b3c4d/isr",
		"production/shop/web/r1a2b3c4d/assets/",
		"production/shop/web/r1a2b3c4d/isr/cache/",
		"production/shop/web/notarelease/",
		"production/shop/web/r1a2b3c4d/isr2/",
		"production//web/r1a2b3c4d/",
	} {
		if got, ok := naming.ISRPrefixUnder(prefix); ok {
			t.Errorf("ISRPrefixUnder(%q) = %q, want no ISR prefix: it covers no one release's ISR objects", prefix, got)
		}
	}
}
