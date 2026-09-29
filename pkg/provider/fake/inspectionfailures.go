package fake

import "sync"

type inspectionFailures struct {
	mu     sync.Mutex
	queued []error
}

func (p *Provider) QueueInspectionFailures(outcomes ...error) {
	p.inspections.mu.Lock()
	defer p.inspections.mu.Unlock()
	p.inspections.queued = append(p.inspections.queued, outcomes...)
}

func (f *inspectionFailures) next() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.queued) == 0 {
		return nil
	}
	outcome := f.queued[0]
	f.queued = f.queued[1:]
	return outcome
}
