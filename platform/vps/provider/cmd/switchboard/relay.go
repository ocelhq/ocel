package main

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"slices"
	"time"

	"github.com/ocelhq/ocel/platform/vps/provider/switchboard"
)

const networkLookup = 10 * time.Second

type resolving func(ctx context.Context, name string) ([]netip.Addr, error)

func relayOf(ctx context.Context, relaying, networks []string) ([]netip.Prefix, error) {
	var relayed []netip.Prefix
	for _, spelled := range relaying {
		if addr, err := netip.ParseAddr(spelled); err == nil {
			relayed = append(relayed, netip.PrefixFrom(addr.Unmap(), addr.Unmap().BitLen()))
			continue
		}
		prefix, err := netip.ParsePrefix(spelled)
		if err != nil {
			return nil, fmt.Errorf("--relay %q is neither an address nor a prefix", spelled)
		}
		relayed = append(relayed, prefix.Masked())
	}
	for _, network := range networks {
		asking, stop := context.WithTimeout(ctx, networkLookup)
		held, err := networkHeld(asking, network, func(ctx context.Context, name string) ([]netip.Addr, error) {
			return net.DefaultResolver.LookupNetIP(ctx, "ip", name)
		}, net.InterfaceAddrs)
		stop()
		if err != nil {
			return nil, err
		}
		relayed = append(relayed, held...)
	}
	return relayed, nil
}

func networkHeld(ctx context.Context, network string, resolve resolving, addresses func() ([]net.Addr, error)) ([]netip.Prefix, error) {
	if network == "" {
		return nil, fmt.Errorf("--relay-network names no network")
	}
	named := switchboard.Name + "." + network
	resolved, err := resolve(ctx, named)
	if err != nil {
		return nil, fmt.Errorf("--relay-network %s: %s resolves to nothing: %w", network, named, err)
	}
	for i, addr := range resolved {
		resolved[i] = addr.Unmap()
	}
	held, err := addresses()
	if err != nil {
		return nil, fmt.Errorf("--relay-network %s: %w", network, err)
	}
	var prefixes []netip.Prefix
	for _, addr := range held {
		interfaced, ok := addr.(*net.IPNet)
		if !ok {
			continue
		}
		ip, ok := netip.AddrFromSlice(interfaced.IP)
		if !ok || !slices.Contains(resolved, ip.Unmap()) {
			continue
		}
		bits, _ := interfaced.Mask.Size()
		prefixes = append(prefixes, netip.PrefixFrom(ip.Unmap(), bits).Masked())
	}
	if len(prefixes) == 0 {
		return nil, fmt.Errorf("--relay-network %s: %s resolves to %v, which no interface of this switchboard holds", network, named, resolved)
	}
	return prefixes, nil
}
