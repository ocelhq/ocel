package main

import (
	"context"
	"fmt"
	"net"
	"net/netip"
)

func httpsBinds(ctx context.Context, spelled string, resolve resolving, addresses func() ([]net.Addr, error)) ([]string, error) {
	host, port, err := net.SplitHostPort(spelled)
	if err != nil {
		return nil, fmt.Errorf("--https-listen %q is no host:port or network:port: %w", spelled, err)
	}
	addr, err := netip.ParseAddr(host)
	if host == "" || err == nil && addr.WithZone("").Unmap().IsUnspecified() {
		return nil, fmt.Errorf("--https-listen %q binds every interface the switchboard has, each project network among them; name one address or a docker network", spelled)
	}
	if err == nil {
		return []string{spelled}, nil
	}
	asking, stop := context.WithTimeout(ctx, networkLookup)
	defer stop()
	prefixes, err := prefixesOn(asking, host, resolve, addresses)
	if err != nil {
		return nil, fmt.Errorf("--https-listen %w", err)
	}
	binds := make([]string, 0, len(prefixes))
	for _, prefix := range prefixes {
		binds = append(binds, net.JoinHostPort(prefix.Addr().String(), port))
	}
	return binds, nil
}
