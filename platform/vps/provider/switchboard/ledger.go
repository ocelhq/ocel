package switchboard

import (
	"net"
	"sync"
)

type ledger struct {
	mu      sync.Mutex
	flights map[string]*flight
}

type flight struct {
	requests int
	hijacked map[net.Conn]bool
	quiet    []chan struct{}
}

func (l *ledger) held(address string) *flight {
	if l.flights == nil {
		l.flights = map[string]*flight{}
	}
	held, ok := l.flights[address]
	if !ok {
		held = &flight{hijacked: map[net.Conn]bool{}}
		l.flights[address] = held
	}
	return held
}

func (l *ledger) take(address string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.held(address).requests++
}

func (l *ledger) drop(address string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	held := l.held(address)
	held.requests--
	if held.requests > 0 {
		return
	}
	for _, quiet := range held.quiet {
		close(quiet)
	}
	delete(l.flights, address)
}

func (l *ledger) hijack(address string, conn net.Conn) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.held(address).hijacked[conn] = true
}

func (l *ledger) release(address string, conn net.Conn) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if held, ok := l.flights[address]; ok {
		delete(held.hijacked, conn)
	}
}

func (l *ledger) cut(address string) {
	l.mu.Lock()
	var conns []net.Conn
	if held, ok := l.flights[address]; ok {
		for conn := range held.hijacked {
			conns = append(conns, conn)
		}
	}
	l.mu.Unlock()
	for _, conn := range conns {
		_ = conn.Close()
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
