package gateway

import "sync"

type hub struct {
	mu       sync.RWMutex
	channels map[string]map[subscriber]struct{}
}

type subscriber struct {
	id    string
	queue *queue
}

func newHub() *hub {
	return &hub{channels: map[string]map[subscriber]struct{}{}}
}

func (h *hub) Subscribe(channel, id string, outbound *queue) (unsubscribe func()) {
	s := subscriber{id: id, queue: outbound}
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

func (h *hub) Publish(channel, event string) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for _, subscribed := range subscribedChannels(channel) {
		for s := range h.channels[subscribed] {
			s.queue.Offer(encodeData(s.id, event))
		}
	}
}

const wildcardSuffix = "/*"

func subscribedChannels(channel string) []string {
	subscribed := []string{channel}
	for i := len(channel) - 1; i > 0; i-- {
		if channel[i] == '/' {
			subscribed = append(subscribed, channel[:i]+wildcardSuffix)
		}
	}
	return subscribed
}

func encodeData(id, event string) []byte {
	return mustEncode(dataFrame{Type: frameData, ID: id, Event: event})
}
