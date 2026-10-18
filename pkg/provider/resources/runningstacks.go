package resources

import (
	"sync"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
)

type RunningStacks struct {
	mu      sync.Mutex
	running map[provider.StackRef]bool
}

func (r *RunningStacks) Claim(ref provider.StackRef) (func(), error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.running[ref] {
		return nil, refusal.Refuse(refusal.CodeBusy,
			"%s is being provisioned or destroyed by another run of this provider, and two runs writing one stack at once leave it as neither planned it; "+
				"run this again once that run finishes", ref.Name)
	}
	if r.running == nil {
		r.running = map[provider.StackRef]bool{}
	}
	r.running[ref] = true
	return func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		delete(r.running, ref)
	}, nil
}
