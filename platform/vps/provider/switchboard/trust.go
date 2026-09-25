package switchboard

import (
	"context"
	"net"
	"net/netip"
	"slices"
	"sync"
	"sync/atomic"
	"time"
)

const (
	trustRefresh = time.Second
	trustLookup  = 2 * time.Second
)

type lookup func(ctx context.Context, name string) ([]netip.Addr, error)

type Trust struct {
	Prefixes []netip.Prefix
	Names    []string
}

type trustedPeers struct {
	prefixes []netip.Prefix
	names    []string
	named    atomic.Pointer[[]netip.Addr]

	mu       sync.Mutex
	lookUp   lookup
	every    time.Duration
	asked    time.Time
	resolved chan struct{}
}

func trusting(given Trust) *trustedPeers {
	return &trustedPeers{
		prefixes: slices.Clone(given.Prefixes),
		names:    slices.Clone(given.Names),
		lookUp: func(ctx context.Context, name string) ([]netip.Addr, error) {
			return net.DefaultResolver.LookupNetIP(ctx, "ip", name)
		},
		every: trustRefresh,
	}
}

func (t *trustedPeers) trusts(ctx context.Context, peer netip.Addr) bool {
	if slices.ContainsFunc(t.prefixes, func(prefix netip.Prefix) bool { return prefix.Contains(peer) }) {
		return true
	}
	if len(t.names) == 0 {
		return false
	}
	if t.namedAt(peer) {
		return true
	}
	resolved := t.refreshed(ctx)
	if resolved == nil {
		return false
	}
	select {
	case <-resolved:
		return t.namedAt(peer)
	case <-ctx.Done():
		return false
	}
}

func (t *trustedPeers) namedAt(peer netip.Addr) bool {
	named := t.named.Load()
	return named != nil && slices.Contains(*named, peer)
}

func (t *trustedPeers) refreshed(ctx context.Context) <-chan struct{} {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.resolved != nil {
		return t.resolved
	}
	if !t.asked.IsZero() && time.Since(t.asked) < t.every {
		return nil
	}
	t.asked = time.Now()
	resolved := make(chan struct{})
	t.resolved = resolved
	go t.resolve(context.WithoutCancel(ctx), t.lookUp, resolved)
	return resolved
}

func (t *trustedPeers) resolve(ctx context.Context, lookUp lookup, resolved chan struct{}) {
	asking, stop := context.WithTimeout(ctx, trustLookup)
	defer stop()
	var named []netip.Addr
	answered := false
	for _, name := range t.names {
		found, err := lookUp(asking, name)
		if err != nil {
			continue
		}
		answered = true
		for _, addr := range found {
			named = append(named, addr.Unmap())
		}
	}
	if answered {
		t.named.Store(&named)
	}
	t.mu.Lock()
	t.resolved = nil
	t.mu.Unlock()
	close(resolved)
}
