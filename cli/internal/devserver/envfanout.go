package devserver

import "sync"

type envFanout struct {
	mu          sync.Mutex
	latest      map[string]string
	hasLatest   bool
	subscribers map[chan map[string]string]struct{}
}

func newEnvFanout() *envFanout {
	return &envFanout{subscribers: make(map[chan map[string]string]struct{})}
}

func (f *envFanout) push(env map[string]string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.latest = env
	f.hasLatest = true
	for ch := range f.subscribers {
		select {
		case ch <- env:
		default:
		}
	}
}

func (f *envFanout) subscribe() chan map[string]string {
	ch := make(chan map[string]string, 1)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.hasLatest {
		ch <- f.latest
	}
	f.subscribers[ch] = struct{}{}
	return ch
}

func (f *envFanout) unsubscribe(ch chan map[string]string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.subscribers, ch)
}
