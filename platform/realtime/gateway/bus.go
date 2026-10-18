package gateway

import (
	"context"
	"sync"
)

type Bus interface {
	Publish(ctx context.Context, channel, event string) error
	Listen(deliver func(channel, event string)) (stop func())
}

type MemoryBus struct {
	mu        sync.RWMutex
	listeners map[*listener]struct{}
}

type listener struct {
	deliver func(channel, event string)
}

func NewMemoryBus() *MemoryBus {
	return &MemoryBus{listeners: map[*listener]struct{}{}}
}

func (b *MemoryBus) Publish(_ context.Context, channel, event string) error {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for l := range b.listeners {
		l.deliver(channel, event)
	}
	return nil
}

func (b *MemoryBus) Listen(deliver func(channel, event string)) func() {
	l := &listener{deliver: deliver}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.listeners[l] = struct{}{}
	return func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		delete(b.listeners, l)
	}
}
