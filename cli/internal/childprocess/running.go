package childprocess

import "sync"

type runningChildren struct {
	mu       sync.Mutex
	children map[*Child]struct{}
}

var running = &runningChildren{children: map[*Child]struct{}{}}

func (r *runningChildren) add(child *Child) {
	r.mu.Lock()
	r.children[child] = struct{}{}
	r.mu.Unlock()
}

func (r *runningChildren) remove(child *Child) {
	r.mu.Lock()
	delete(r.children, child)
	r.mu.Unlock()
}

func KillAll() {
	running.mu.Lock()
	children := make([]*Child, 0, len(running.children))
	for child := range running.children {
		children = append(children, child)
	}
	running.mu.Unlock()

	for _, child := range children {
		if !child.hasExited() {
			_ = child.kill()
		}
		child.terminal.restore()
	}
}
