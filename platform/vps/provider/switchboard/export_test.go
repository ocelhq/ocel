package switchboard

import (
	"context"
	"net/netip"
	"time"
)

func (b *Board) DialConnectorAt(socket string) { b.connector = socket }

func (b *Board) RefreshTrustEvery(every time.Duration) {
	b.trust.mu.Lock()
	defer b.trust.mu.Unlock()
	b.trust.every = every
}

func (b *Board) LookUpNamesWith(lookUp func(ctx context.Context, name string) ([]netip.Addr, error)) {
	b.trust.mu.Lock()
	defer b.trust.mu.Unlock()
	b.trust.lookUp = lookUp
}
