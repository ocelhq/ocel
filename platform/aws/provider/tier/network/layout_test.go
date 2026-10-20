package network

import (
	"errors"
	"net/netip"
	"slices"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/refusal"
)

func TestEachTierOfANamespaceTriesEveryDefaultSixteenOnceStartingFromOneDerivedFromTheNamespace(t *testing.T) {
	t.Parallel()

	tenNet := netip.MustParsePrefix("10.0.0.0/8")
	for _, namespace := range []string{"ocel", "acme", "acme-staging", "a"} {
		production, preview := DefaultCIDRs(namespace, environment.TierProduction), DefaultCIDRs(namespace, environment.TierPreview)
		if again := DefaultCIDRs(namespace, environment.TierProduction); !slices.Equal(again, production) {
			t.Errorf("namespace %s tries %v and then %v, want the same blocks in the same order every run", namespace, production, again)
		}
		if production[0] != DefaultCIDR(namespace, environment.TierProduction) {
			t.Errorf("namespace %s tries %s first, want its default %s", namespace, production[0], DefaultCIDR(namespace, environment.TierProduction))
		}
		tried := map[string]bool{}
		for i, block := range append(slices.Clone(production), preview...) {
			parsed := netip.MustParsePrefix(block)
			if parsed.Bits() != 16 || !tenNet.Contains(parsed.Addr()) || parsed.Addr().As4()[1] < 16 {
				t.Errorf("namespace %s tries %s, want a /16 in 10.16.0.0 to 10.255.0.0", namespace, block)
			}
			if _, err := NewLayout(block); err != nil {
				t.Errorf("namespace %s tries %s, which NewLayout refuses: %v", namespace, block, err)
			}
			if tried[block] {
				t.Errorf("namespace %s tries %s twice (candidate %d)", namespace, block, i)
			}
			tried[block] = true
		}
		for i := range production {
			if second := netip.MustParsePrefix(production[i]).Addr().As4()[1]; netip.MustParsePrefix(preview[i]).Addr().As4()[1] != second+1 {
				t.Errorf("namespace %s tries %s and %s, want each preview block right after production's", namespace, production[i], preview[i])
			}
		}
	}
	if DefaultCIDR("ocel", environment.TierProduction) == DefaultCIDR("acme", environment.TierProduction) {
		t.Errorf("namespaces ocel and acme both default to %s, want them apart so a second namespace in a region rarely walks past its first block", DefaultCIDR("ocel", environment.TierProduction))
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
