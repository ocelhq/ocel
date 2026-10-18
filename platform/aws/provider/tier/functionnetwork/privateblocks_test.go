package functionnetwork

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/refusal"
)

func TestPrivateBlocksCountDownFromTheTopOfThePrimaryCIDR(t *testing.T) {
	t.Parallel()

	got, err := pickPrivateBlocks("172.31.0.0/16", []string{"172.31.0.0/20", "172.31.16.0/20", "172.31.32.0/20"}, 2)
	if err != nil {
		t.Fatalf("pickPrivateBlocks() = %v", err)
	}
	if want := []string{"172.31.240.0/20", "172.31.224.0/20"}; !slices.Equal(got, want) {
		t.Errorf("pickPrivateBlocks() = %v, want %v", got, want)
	}
}

func TestPrivateBlocksSkipEveryBlockAnExistingSubnetOverlaps(t *testing.T) {
	t.Parallel()

	taken := []string{"172.31.240.0/24", "172.31.208.0/20", "172.31.232.0/21"}
	got, err := pickPrivateBlocks("172.31.0.0/16", taken, 2)
	if err != nil {
		t.Fatalf("pickPrivateBlocks() = %v", err)
	}
	if want := []string{"172.31.192.0/20", "172.31.176.0/20"}; !slices.Equal(got, want) {
		t.Errorf("pickPrivateBlocks() = %v, want %v: a block that overlaps a /24, a /21 or a whole /20 already there is not free", got, want)
	}
}

func TestPrivateBlocksStayInsideASmallerPrimaryCIDR(t *testing.T) {
	t.Parallel()

	got, err := pickPrivateBlocks("10.0.0.0/18", nil, 2)
	if err != nil {
		t.Fatalf("pickPrivateBlocks() = %v", err)
	}
	if want := []string{"10.0.48.0/20", "10.0.32.0/20"}; !slices.Equal(got, want) {
		t.Errorf("pickPrivateBlocks() = %v, want %v", got, want)
	}
}

func TestPrivateBlocksRefuseAVPCWithNoFreeBlocksLeft(t *testing.T) {
	t.Parallel()

	_, err := pickPrivateBlocks("10.0.0.0/19", []string{"10.0.0.0/20"}, 2)
	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeInvalid {
		t.Fatalf("pickPrivateBlocks() = %v, want a refusal", err)
	}
	for _, want := range []string{"10.0.0.0/19", "2", "/20"} {
		if !strings.Contains(refused.Error(), want) {
			t.Errorf("refusal %q does not name %q", refused.Error(), want)
		}
	}
}

func TestPrivateBlocksRefuseAPrimaryCIDRTooSmallForOne(t *testing.T) {
	t.Parallel()

	if _, err := pickPrivateBlocks("10.0.0.0/24", nil, 2); err == nil {
		t.Fatal("pickPrivateBlocks() accepted a /24, which holds no /20 block")
	}
}

func TestPrivateBlocksRefuseAnUnreadableCIDR(t *testing.T) {
	t.Parallel()

	if _, err := pickPrivateBlocks("", nil, 2); err == nil {
		t.Fatal("pickPrivateBlocks() accepted a VPC with no primary CIDR")
	}
	if _, err := pickPrivateBlocks("172.31.0.0/16", []string{"not-a-cidr"}, 2); err == nil {
		t.Fatal("pickPrivateBlocks() accepted a subnet with no readable CIDR")
	}
}
