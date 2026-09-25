package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"strings"

	"github.com/ocelhq/ocel/platform/vps/provider/switchboard"
)

const routesPath = "/proc/net/route"

const defaultDestination = "00000000"

func relayOf(relaying []string) ([]netip.Prefix, error) {
	var relayed []netip.Prefix
	for _, spelled := range relaying {
		if spelled == switchboard.OwnNetwork {
			held, err := ownNetworkHere()
			if err != nil {
				return nil, fmt.Errorf("--relay %s: %w", spelled, err)
			}
			relayed = append(relayed, held...)
			continue
		}
		if addr, err := netip.ParseAddr(spelled); err == nil {
			relayed = append(relayed, netip.PrefixFrom(addr.Unmap(), addr.Unmap().BitLen()))
			continue
		}
		prefix, err := netip.ParsePrefix(spelled)
		if err != nil {
			return nil, fmt.Errorf("--relay %q is neither an address, a prefix nor %s", spelled, switchboard.OwnNetwork)
		}
		relayed = append(relayed, prefix.Masked())
	}
	return relayed, nil
}

func ownNetworkHere() ([]netip.Prefix, error) {
	routes, err := os.Open(routesPath)
	if err != nil {
		return nil, err
	}
	defer routes.Close()
	return ownNetwork(routes, func(name string) ([]net.Addr, error) {
		held, err := net.InterfaceByName(name)
		if err != nil {
			return nil, err
		}
		return held.Addrs()
	})
}

func ownNetwork(routes io.Reader, addresses func(name string) ([]net.Addr, error)) ([]netip.Prefix, error) {
	scanner := bufio.NewScanner(routes)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 2 || fields[1] != defaultDestination {
			continue
		}
		held, err := addresses(fields[0])
		if err != nil {
			return nil, err
		}
		var prefixes []netip.Prefix
		for _, addr := range held {
			network, ok := addr.(*net.IPNet)
			if !ok {
				continue
			}
			ip, ok := netip.AddrFromSlice(network.IP)
			if !ok {
				continue
			}
			bits, _ := network.Mask.Size()
			prefixes = append(prefixes, netip.PrefixFrom(ip.Unmap(), bits).Masked())
		}
		if len(prefixes) == 0 {
			return nil, fmt.Errorf("%s carries the default route and holds no address", fields[0])
		}
		return prefixes, nil
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return nil, errors.New("no default route leaves this switchboard, so it sits on no network to relay from")
}
