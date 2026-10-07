package childprocess

import "sync"

type runningChildren struct {
	mu       sync.Mutex
	children map[*Child]struct{}
	groups   map[*Group]struct{}
}

var running = &runningChildren{children: map[*Child]struct{}{}, groups: map[*Group]struct{}{}}

func (r *runningChildren) addGroup(group *Group) {
	r.mu.Lock()
	r.groups[group] = struct{}{}
	r.mu.Unlock()
}

func (r *runningChildren) removeGroup(group *Group) {
	r.mu.Lock()
	delete(r.groups, group)
	r.mu.Unlock()
}

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
	groups := make([]*Group, 0, len(running.groups))
	for group := range running.groups {
		groups = append(groups, group)
	}
	running.mu.Unlock()

	for _, group := range groups {
		_ = KillGroup(group.cmd)
	}

	for _, child := range children {
		if !child.hasExited() {
			_ = child.kill()
		}
		child.terminal.restore()
	}
}
