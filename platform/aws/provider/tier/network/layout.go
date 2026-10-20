package network

import (
	"hash/fnv"
	"net/netip"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/refusal"
)

const (
	Zones = 2

	defaultSecondOctetFrom = 16
	defaultBlockPairs      = 120
	exampleCIDR            = "10.64.0.0/16"

	widestBits     = 16
	narrowestBits  = 24
	subnetSlotBits = 3
)

var privateRanges = []netip.Prefix{
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.168.0.0/16"),
}

type Layout struct {
	CIDR     string
	Public   []string
	Private  []string
	Isolated []string
}

func DefaultCIDR(namespace string, tier environment.Tier) string {
	return DefaultCIDRs(namespace, tier)[0]
}

func DefaultCIDRs(namespace string, tier environment.Tier) []string {
	sum := fnv.New32a()
	_, _ = sum.Write([]byte(namespace))
	first := int(sum.Sum32() % defaultBlockPairs)
	blocks := make([]string, 0, defaultBlockPairs)
	for step := range defaultBlockPairs {
		second := defaultSecondOctetFrom + 2*((first+step)%defaultBlockPairs)
		if tier != environment.TierProduction {
			second++
		}
		blocks = append(blocks, netip.PrefixFrom(netip.AddrFrom4([4]byte{10, byte(second), 0, 0}), widestBits).String())
	}
	return blocks
}

func NewLayout(cidr string) (Layout, error) {
	block, err := netip.ParsePrefix(cidr)
	if err != nil || !block.Addr().Is4() || block.Masked() != block {
		return Layout{}, refusal.Refuse(refusal.CodeInvalid,
			"the network's block %q is not an IPv4 CIDR block such as %s", cidr, exampleCIDR)
	}
	if block.Bits() < widestBits || block.Bits() > narrowestBits {
		return Layout{}, refusal.Refuse(refusal.CodeInvalid,
			"the network's block %s is a /%d, and ocel splits a VPC between /%d and /%d into the six subnets it places across %d zones",
			cidr, block.Bits(), widestBits, narrowestBits, Zones)
	}
	if !isPrivate(block) {
		return Layout{}, refusal.Refuse(refusal.CodeInvalid,
			"the network's block %s lies outside the private ranges 10.0.0.0/8, 172.16.0.0/12 and 192.168.0.0/16", cidr)
	}
	slots := carve(block)
	return Layout{
		CIDR:     cidr,
		Public:   slots[0:Zones],
		Private:  slots[Zones : 2*Zones],
		Isolated: slots[2*Zones : 3*Zones],
	}, nil
}

func isPrivate(block netip.Prefix) bool {
	for _, private := range privateRanges {
		if private.Bits() <= block.Bits() && private.Contains(block.Addr()) {
			return true
		}
	}
	return false
}

func carve(block netip.Prefix) []string {
	bits := block.Bits() + subnetSlotBits
	size := uint32(1) << (32 - bits)
	base := addrToUint(block.Addr())
	slots := make([]string, 0, 1<<subnetSlotBits)
	for index := range uint32(1) << subnetSlotBits {
		slots = append(slots, netip.PrefixFrom(uintToAddr(base+index*size), bits).String())
	}
	return slots
}

func addrToUint(addr netip.Addr) uint32 {
	octets := addr.As4()
	return uint32(octets[0])<<24 | uint32(octets[1])<<16 | uint32(octets[2])<<8 | uint32(octets[3])
}

func uintToAddr(value uint32) netip.Addr {
	return netip.AddrFrom4([4]byte{byte(value >> 24), byte(value >> 16), byte(value >> 8), byte(value)})
}
