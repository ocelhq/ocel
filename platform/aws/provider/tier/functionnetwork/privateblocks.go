package functionnetwork

import (
	"fmt"
	"net/netip"

	"github.com/ocelhq/ocel/pkg/refusal"
)

const privateBlockBits = 20

func pickPrivateBlocks(primary string, taken []string, count int) ([]string, error) {
	vpc, err := netip.ParsePrefix(primary)
	if err != nil {
		return nil, fmt.Errorf("read the primary CIDR %q of the default VPC: %w", primary, err)
	}
	vpc = vpc.Masked()
	if !vpc.Addr().Is4() || vpc.Bits() > privateBlockBits {
		return nil, refusal.Refuse(refusal.CodeInvalid,
			"the default VPC's primary CIDR %s is too small to carve a /%d private subnet from", primary, privateBlockBits)
	}
	occupied := make([]netip.Prefix, 0, len(taken))
	for _, cidr := range taken {
		prefix, err := netip.ParsePrefix(cidr)
		if err != nil {
			return nil, fmt.Errorf("read the CIDR %q of a subnet in the default VPC: %w", cidr, err)
		}
		occupied = append(occupied, prefix.Masked())
	}

	blocks := uint32(1) << (privateBlockBits - vpc.Bits())
	size := uint32(1) << (32 - privateBlockBits)
	base := addrToUint(vpc.Addr())
	var picked []string
	for index := blocks; index > 0 && len(picked) < count; index-- {
		block := netip.PrefixFrom(uintToAddr(base+(index-1)*size), privateBlockBits)
		if overlapsAny(block, occupied) {
			continue
		}
		picked = append(picked, block.String())
	}
	if len(picked) < count {
		return nil, refusal.Refuse(refusal.CodeInvalid,
			"the default VPC's primary CIDR %s has %d free /%d blocks left and the function network needs %d for its private subnets; free the address space or run the app as a container app",
			primary, len(picked), privateBlockBits, count)
	}
	return picked, nil
}

func overlapsAny(block netip.Prefix, occupied []netip.Prefix) bool {
	for _, other := range occupied {
		if block.Overlaps(other) {
			return true
		}
	}
	return false
}

func addrToUint(addr netip.Addr) uint32 {
	octets := addr.As4()
	return uint32(octets[0])<<24 | uint32(octets[1])<<16 | uint32(octets[2])<<8 | uint32(octets[3])
}

func uintToAddr(value uint32) netip.Addr {
	return netip.AddrFrom4([4]byte{byte(value >> 24), byte(value >> 16), byte(value >> 8), byte(value)})
}
