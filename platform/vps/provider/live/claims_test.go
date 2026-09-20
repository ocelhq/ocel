package live_test

import (
	"testing"

	"github.com/ocelhq/ocel/platform/vps/provider/live"
)

func TestAClaimSurvivesBeingNamedAndReadBack(t *testing.T) {
	t.Parallel()

	for _, held := range []live.Claimed{
		{Owner: "ocel.shop.production", Hostname: "shop.example.com", Pointer: "@production"},
		{Owner: "ocel.shop.production", Hostname: "storage.shop.example.com", Pointer: "@production", App: "storage"},
	} {
		read, mine := live.ClaimedAt(live.ClaimIdentity(held))
		if !mine || read != held {
			t.Errorf("ClaimedAt(ClaimIdentity(%+v)) = %+v, %v", held, read, mine)
		}
	}
}

func TestARouteThatIsNotAClaimIsNoClaim(t *testing.T) {
	t.Parallel()

	for _, identity := range []string{"ocel-app-ocel.shop.production/@production/web", "ocel-box", "ocel-host-only/two"} {
		if read, mine := live.ClaimedAt(identity); mine {
			t.Errorf("ClaimedAt(%q) = %+v, want nothing", identity, read)
		}
	}
}

const claimedConfig = `{"apps":{"http":{"servers":{"ocel":{"routes":[
	{"@id":"ocel-host-ocel.shop.production/shop.example.com/@production/web"},
	{"@id":"ocel-host-ocel.shop.production/storage.shop.example.com/@production/storage"},
	{"@id":"ocel-host-ocel.other.production/storage.other.example.com/@production/storage"},
	{"@id":"ocel-box"}]}}}}}`

func TestTheStoresBaseIsTheHostTheSurfaceClaimsForIt(t *testing.T) {
	t.Parallel()

	claims, err := live.ClaimedIn([]byte(claimedConfig))
	if err != nil {
		t.Fatalf("ClaimedIn() = %v", err)
	}
	if base := live.StoreBase(claims, "ocel.shop.production", "@production"); base != "https://storage.shop.example.com" {
		t.Errorf("StoreBase() = %q, want the hostname this surface claims for its store", base)
	}
	if base := live.StoreBase(claims, "ocel.nothing.production", "@production"); base != "" {
		t.Errorf("StoreBase() = %q for a surface that claims nothing, want nothing", base)
	}
}
