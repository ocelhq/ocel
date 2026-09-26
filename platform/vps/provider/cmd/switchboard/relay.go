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

func systemResolve(ctx context.Context, name string) ([]netip.Addr, error) {
	return net.DefaultResolver.LookupNetIP(ctx, "ip", name)
}

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
		onNetwork, err := networkPrefixes(asking, network, systemResolve, net.InterfaceAddrs)
		stop()
		if err != nil {
			return nil, err
		}
		relayed = append(relayed, onNetwork...)
	}
	return relayed, nil
}

func networkPrefixes(ctx context.Context, network string, resolve resolving, addresses func() ([]net.Addr, error)) ([]netip.Prefix, error) {
	prefixes, err := prefixesOn(ctx, network, resolve, addresses)
	if err != nil {
		return nil, fmt.Errorf("--relay-network %w", err)
	}
	for i, prefix := range prefixes {
		prefixes[i] = prefix.Masked()
	}
	return prefixes, nil
}

func prefixesOn(ctx context.Context, network string, resolve resolving, addresses func() ([]net.Addr, error)) ([]netip.Prefix, error) {
	if network == "" {
		return nil, fmt.Errorf("names no network")
	}
	named := switchboard.Name + "." + network
	resolved, err := resolve(ctx, named)
	if err != nil {
		return nil, fmt.Errorf("%s: %s resolves to nothing: %w", network, named, err)
	}
	for i, addr := range resolved {
		resolved[i] = addr.Unmap()
	}
	interfaceAddrs, err := addresses()
	if err != nil {
		return nil, fmt.Errorf("%s: %w", network, err)
	}
	var prefixes []netip.Prefix
	for _, addr := range interfaceAddrs {
		interfaced, ok := addr.(*net.IPNet)
		if !ok {
			continue
		}
		ip, ok := netip.AddrFromSlice(interfaced.IP)
		if !ok || !slices.Contains(resolved, ip.Unmap()) {
			continue
		}
		bits, _ := interfaced.Mask.Size()
		prefixes = append(prefixes, netip.PrefixFrom(ip.Unmap(), bits))
	}
	if len(prefixes) == 0 {
		return nil, fmt.Errorf("%s: %s resolves to %v, which no interface of this switchboard has", network, named, resolved)
	}
	return prefixes, nil
}
