package network

import (
	"errors"
	"net/netip"
	"slices"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/refusal"
)

func TestEachTierOfANamespaceGetsAPrivateSixteenOfItsOwnDerivedFromTheNamespaceWhenTheConfigNamesNone(t *testing.T) {
	t.Parallel()

	tenNet := netip.MustParsePrefix("10.0.0.0/8")
	for _, namespace := range []string{"ocel", "acme", "acme-staging", "a"} {
		production, preview := CIDRFor(namespace, environment.TierProduction, ""), CIDRFor(namespace, environment.TierPreview, "")
		if again := CIDRFor(namespace, environment.TierProduction, ""); again != production {
			t.Errorf("namespace %s defaults to %s and then %s, want the same block every run", namespace, production, again)
		}
		for _, block := range []string{production, preview} {
			parsed := netip.MustParsePrefix(block)
			if parsed.Bits() != 16 || !tenNet.Contains(parsed.Addr()) || parsed.Addr().As4()[1] < 16 {
				t.Errorf("namespace %s defaults to %s, want a /16 in 10.16.0.0 to 10.255.0.0", namespace, block)
			}
			if _, err := NewLayout(block); err != nil {
				t.Errorf("namespace %s defaults to %s, which NewLayout refuses: %v", namespace, block, err)
			}
		}
		if second := netip.MustParsePrefix(production).Addr().As4()[1]; netip.MustParsePrefix(preview).Addr().As4()[1] != second+1 {
			t.Errorf("namespace %s defaults to %s and %s, want preview's block right after production's", namespace, production, preview)
		}
	}
	if CIDRFor("ocel", environment.TierProduction, "") == CIDRFor("acme", environment.TierProduction, "") {
		t.Errorf("namespaces ocel and acme both default to %s, want them apart so a second namespace in a region needs no block of its own", CIDRFor("ocel", environment.TierProduction, ""))
	}
	if got := CIDRFor("ocel", environment.TierProduction, "192.168.0.0/20"); got != "192.168.0.0/20" {
		t.Errorf("CIDRFor() = %s, want the block the config names", got)
	}
}

func TestALayoutCarvesAPublicAPrivateAndAnIsolatedSubnetPerZoneFromTheBlock(t *testing.T) {
	t.Parallel()

	layout, err := NewLayout("10.20.0.0/16")
	if err != nil {
		t.Fatalf("NewLayout() = %v", err)
	}
	want := Layout{
		CIDR:     "10.20.0.0/16",
		Public:   []string{"10.20.0.0/19", "10.20.32.0/19"},
		Private:  []string{"10.20.64.0/19", "10.20.96.0/19"},
		Isolated: []string{"10.20.128.0/19", "10.20.160.0/19"},
	}
	if layout.CIDR != want.CIDR || !slices.Equal(layout.Public, want.Public) || !slices.Equal(layout.Private, want.Private) || !slices.Equal(layout.Isolated, want.Isolated) {
		t.Errorf("NewLayout() = %+v, want %+v", layout, want)
	}
}

func TestALayoutIsRefusedForABlockAVPCCannotHoldOrThatIsNotPrivate(t *testing.T) {
	t.Parallel()

	for _, cidr := range []string{"", "10.20.0.0", "10.0.0.0/8", "10.20.0.0/25", "8.8.0.0/16", "172.32.0.0/16", "fd00::/48", "10.20.0.1/16"} {
		_, err := NewLayout(cidr)
		var refused refusal.Refusal
		if !errors.As(err, &refused) {
			t.Errorf("NewLayout(%q) = %v, want a refusal", cidr, err)
		}
	}
	for _, cidr := range []string{"172.16.0.0/16", "192.168.10.0/24"} {
		if _, err := NewLayout(cidr); err != nil {
			t.Errorf("NewLayout(%q) = %v, want a private block between /16 and /24 accepted", cidr, err)
		}
	}
}
