package network

import (
	"errors"
	"slices"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/refusal"
)

func TestEachTierGetsAFixedPrivateBlockOfItsOwnWhenTheConfigNamesNone(t *testing.T) {
	t.Parallel()

	production, preview := CIDRFor(environment.TierProduction, ""), CIDRFor(environment.TierPreview, "")
	if production != "10.20.0.0/16" || preview != "10.21.0.0/16" {
		t.Errorf("default blocks = %s, %s, want 10.20.0.0/16 for production and 10.21.0.0/16 for preview", production, preview)
	}
	if got := CIDRFor(environment.TierProduction, "192.168.0.0/20"); got != "192.168.0.0/20" {
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
