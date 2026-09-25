package switchboard

import (
	"context"
	"net"
	"net/netip"
	"slices"
	"sync"
	"time"
)

const (
	trustRefresh = time.Second
	trustLookup  = 2 * time.Second
)

type Resolver func(ctx context.Context, name string) ([]netip.Addr, error)

type Trust struct {
	Prefixes []netip.Prefix
	Names    []string
	Resolve  Resolver
}

type trust struct {
	prefixes []netip.Prefix
	names    []string
	resolve  Resolver
	every    time.Duration
	mu       sync.Mutex
	held     []netip.Addr
	asked    time.Time
}

func trusting(given Trust) *trust {
	resolve := given.Resolve
	if resolve == nil {
		resolve = func(ctx context.Context, name string) ([]netip.Addr, error) {
			return net.DefaultResolver.LookupNetIP(ctx, "ip", name)
		}
	}
	return &trust{prefixes: slices.Clone(given.Prefixes), names: slices.Clone(given.Names), resolve: resolve, every: trustRefresh}
}

func (t *trust) trusts(ctx context.Context, peer netip.Addr) bool {
	if slices.ContainsFunc(t.prefixes, func(prefix netip.Prefix) bool { return prefix.Contains(peer) }) {
		return true
	}
	if len(t.names) == 0 {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if slices.Contains(t.held, peer) {
		return true
	}
	if !t.asked.IsZero() && time.Since(t.asked) < t.every {
		return false
	}
	t.asked = time.Now()
	asking, stop := context.WithTimeout(context.WithoutCancel(ctx), trustLookup)
	defer stop()
	var held []netip.Addr
	resolved := false
	for _, name := range t.names {
		found, err := t.resolve(asking, name)
		if err != nil {
			continue
		}
		resolved = true
		for _, addr := range found {
			held = append(held, addr.Unmap())
		}
	}
	if resolved {
		t.held = held
	}
	return slices.Contains(t.held, peer)
}
