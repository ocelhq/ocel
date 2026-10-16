package fake

import (
	"sync"

	"github.com/ocelhq/ocel/pkg/provider"
)

type probeFailures struct {
	mu     sync.Mutex
	queued map[string][]error
	last   map[string]provider.ProbeFailure
}

func (p *Provider) QueueProbeFailures(hostname string, causes ...error) {
	p.probes.mu.Lock()
	defer p.probes.mu.Unlock()
	if p.probes.queued == nil {
		p.probes.queued = map[string][]error{}
	}
	p.probes.queued[hostname] = append(p.probes.queued[hostname], causes...)
}

func (f *probeFailures) next(hostname string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	queued := f.queued[hostname]
	if len(queued) == 0 {
		delete(f.last, hostname)
		return nil
	}
	cause := queued[0]
	f.queued[hostname] = queued[1:]
	if f.last == nil {
		f.last = map[string]provider.ProbeFailure{}
	}
	f.last[hostname] = provider.ProbeFailure{Cause: cause.Error()}
	return cause
}

func (f *probeFailures) lastFor(hostname string) provider.ProbeFailure {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.last[hostname]
}
