package switchboard

import (
	"context"
	"sync"
)

type ledger struct {
	mu      sync.Mutex
	flights map[string]*flight
}

type flight struct {
	calls map[*call]struct{}
	quiet []chan struct{}
}

type call struct {
	cancel context.CancelFunc
}

func (l *ledger) take(address string, asked context.Context) (context.Context, func()) {
	ctx, cancel := context.WithCancel(asked)
	placed := &call{cancel: cancel}
	l.mu.Lock()
	if l.flights == nil {
		l.flights = map[string]*flight{}
	}
	upstream, ok := l.flights[address]
	if !ok {
		upstream = &flight{calls: map[*call]struct{}{}}
		l.flights[address] = upstream
	}
	upstream.calls[placed] = struct{}{}
	l.mu.Unlock()
	return ctx, func() {
		cancel()
		l.drop(address, placed)
	}
}

func (l *ledger) drop(address string, placed *call) {
	l.mu.Lock()
	defer l.mu.Unlock()
	upstream := l.flights[address]
	delete(upstream.calls, placed)
	if len(upstream.calls) > 0 {
		return
	}
	for _, quiet := range upstream.quiet {
		close(quiet)
	}
	delete(l.flights, address)
}

func (l *ledger) quiet(address string) <-chan struct{} {
	l.mu.Lock()
	defer l.mu.Unlock()
	quiet := make(chan struct{})
	upstream, ok := l.flights[address]
	if !ok {
		close(quiet)
		return quiet
	}
	upstream.quiet = append(upstream.quiet, quiet)
	return quiet
}

func (l *ledger) inFlight(address string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	if upstream, ok := l.flights[address]; ok {
		return len(upstream.calls)
	}
	return 0
}

func (l *ledger) counts() map[string]int {
	l.mu.Lock()
	defer l.mu.Unlock()
	counted := make(map[string]int, len(l.flights))
	for address, upstream := range l.flights {
		counted[address] = len(upstream.calls)
	}
	return counted
}

func (l *ledger) cut(address string) {
	l.mu.Lock()
	var cancels []context.CancelFunc
	if upstream, ok := l.flights[address]; ok {
		for placed := range upstream.calls {
			cancels = append(cancels, placed.cancel)
		}
	}
	l.mu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
}

func (l *ledger) cutAll() {
	l.mu.Lock()
	var addresses []string
	for address := range l.flights {
		addresses = append(addresses, address)
	}
	l.mu.Unlock()
	for _, address := range addresses {
		l.cut(address)
	}
}
