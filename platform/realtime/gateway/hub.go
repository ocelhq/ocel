package gateway

import "sync"

type Hub struct {
	mu       sync.RWMutex
	channels map[string]map[subscriber]struct{}
}

type subscriber struct {
	id    string
	queue *Queue
}

func NewHub() *Hub {
	return &Hub{channels: map[string]map[subscriber]struct{}{}}
}

func (h *Hub) Subscribe(channel, id string, queue *Queue) (unsubscribe func()) {
	s := subscriber{id: id, queue: queue}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.channels[channel] == nil {
		h.channels[channel] = map[subscriber]struct{}{}
	}
	h.channels[channel][s] = struct{}{}
	return func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		delete(h.channels[channel], s)
		if len(h.channels[channel]) == 0 {
			delete(h.channels, channel)
		}
	}
}

func (h *Hub) Publish(channel, event string) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for s := range h.channels[channel] {
		s.queue.Offer(encodeData(s.id, event))
	}
	for i := len(channel) - 1; i > 0; i-- {
		if channel[i] != '/' {
			continue
		}
		for s := range h.channels[channel[:i]+wildcardSuffix] {
			s.queue.Offer(encodeData(s.id, event))
		}
	}
}

const wildcardSuffix = "/*"

func encodeData(id, event string) []byte {
	return mustEncode(dataFrame{Type: frameData, ID: id, Event: event})
}
