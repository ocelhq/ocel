package runui

import (
	"encoding/hex"
	"time"

	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
)

type stageState int

const (
	stagePending stageState = iota
	stageActive
	stageDone
)

const (
	maxStageNodes     = 4096
	maxOrphanParents  = 4096
	maxOrphanChildren = 256
	maxTreeDepth      = 64
)

type stageNode struct {
	id, parentID string
	linkedParent string
	title        string
	children     []string
	linked       bool

	state      stageState
	message    string
	current    uint32
	total      *uint32
	started    time.Time
	frame      int
	doneFailed bool
	doneDur    time.Duration
}

type stagePlan struct {
	nodes       map[string]*stageNode
	roots       []string
	orphans     map[string][]string
	activeOrder []string

	droppedNodes   int
	droppedOrphans int

	now func() time.Time
}

func newStagePlan() *stagePlan {
	return &stagePlan{
		nodes:   make(map[string]*stageNode),
		orphans: make(map[string][]string),
		now:     time.Now,
	}
}

func (p *stagePlan) useClock(now func() time.Time) {
	p.now = now
}

func stageKey(id []byte) string {
	if len(id) == 0 {
		return ""
	}
	return hex.EncodeToString(id)
}

func (p *stagePlan) apply(ev *progressv1.StagePlanEvent) {
	for _, s := range ev.GetStages() {
		p.declare(s)
	}
}

func (p *stagePlan) declare(s *progressv1.Stage) {
	id := stageKey(s.GetId())
	if id == "" {
		return
	}
	if _, exists := p.nodes[id]; exists {
		return
	}
	n, tracked := p.nodeFor(id)
	if !tracked {
		return
	}
	n.title = s.GetTitle()
	if n.title == "" {
		n.title = phaseLabel(s.GetPhase())
	}
	n.parentID = stageKey(s.GetParentId())
	p.link(n)
}

func (p *stagePlan) nodeFor(id string) (n *stageNode, tracked bool) {
	if n, ok := p.nodes[id]; ok {
		return n, true
	}
	if len(p.nodes) >= maxStageNodes {
		p.droppedNodes++
		return &stageNode{id: id}, false
	}
	n = &stageNode{id: id}
	p.nodes[id] = n
	return n, true
}

func (p *stagePlan) link(n *stageNode) {
	if n.linked {
		return
	}
	if n.parentID == "" || n.parentID == n.id || p.wouldCycle(n.parentID, n.id) {
		p.roots = append(p.roots, n.id)
		n.linkedParent = ""
		n.linked = true
		p.attachOrphans(n.id)
		return
	}
	parent, ok := p.nodes[n.parentID]
	if !ok {
		p.bufferOrphan(n)
		return
	}
	parent.children = append(parent.children, n.id)
	n.linkedParent = n.parentID
	n.linked = true
	p.attachOrphans(n.id)
}

func (p *stagePlan) wouldCycle(candidate, id string) bool {
	for depth := 0; depth < maxTreeDepth; depth++ {
		n, ok := p.nodes[candidate]
		if !ok {
			return false
		}
		if n.id == id {
			return true
		}
		if n.parentID == "" {
			return false
		}
		candidate = n.parentID
	}
	return true
}

func (p *stagePlan) bufferOrphan(n *stageNode) {
	pending, seen := p.orphans[n.parentID]
	if !seen && len(p.orphans) >= maxOrphanParents {
		p.droppedOrphans++
		return
	}
	if len(pending) >= maxOrphanChildren {
		p.droppedOrphans++
		return
	}
	p.orphans[n.parentID] = append(pending, n.id)
}

func (p *stagePlan) attachOrphans(parentID string) {
	pending := p.orphans[parentID]
	if len(pending) == 0 {
		return
	}
	delete(p.orphans, parentID)
	for _, childID := range pending {
		if n, ok := p.nodes[childID]; ok {
			p.link(n)
		}
	}
}

func (p *stagePlan) progress(id, message string, current uint32, total *uint32) (n *stageNode, tracked bool) {
	n, tracked = p.nodeFor(id)
	if n.state != stageActive {
		n.started = p.now()
	}
	n.state = stageActive
	n.message = message
	n.current = current
	if total != nil {
		t := *total
		n.total = &t
	} else {
		n.total = nil
	}
	return n, tracked
}

func (p *stagePlan) restart(id string) {
	n, ok := p.nodes[id]
	if !ok {
		return
	}
	n.started = p.now()
	n.state = stageActive
	n.doneFailed = false
	n.doneDur = 0
	p.ensureActive(id)
}

func (p *stagePlan) ensureActive(id string) {
	for _, existing := range p.activeOrder {
		if existing == id {
			return
		}
	}
	p.activeOrder = append(p.activeOrder, id)
}

func (p *stagePlan) isActive(id string) bool {
	for _, existing := range p.activeOrder {
		if existing == id {
			return true
		}
	}
	return false
}

func (p *stagePlan) removeActive(id string) {
	for i, existing := range p.activeOrder {
		if existing == id {
			p.activeOrder = append(p.activeOrder[:i], p.activeOrder[i+1:]...)
			return
		}
	}
}

func (p *stagePlan) activeSet() map[string]bool {
	active := make(map[string]bool, len(p.activeOrder))
	for _, id := range p.activeOrder {
		active[id] = true
	}
	return active
}

func (p *stagePlan) animating() bool {
	for _, id := range p.activeOrder {
		if n, ok := p.nodes[id]; ok && n.state == stageActive {
			return true
		}
	}
	return false
}

func (p *stagePlan) foldSubtree(id string) {
	p.walk(id, func(n *stageNode) bool {
		if n.id != id {
			p.removeActive(n.id)
		}
		return true
	})
	p.ensureActive(id)
}

func (p *stagePlan) walk(id string, visit func(*stageNode) bool) {
	p.walkFrom(id, 0, visit)
}

func (p *stagePlan) walkFrom(id string, depth int, visit func(*stageNode) bool) {
	n, ok := p.nodes[id]
	if !ok || depth > maxTreeDepth || !visit(n) {
		return
	}
	for _, childID := range n.children {
		p.walkFrom(childID, depth+1, visit)
	}
}

type liveUnit struct {
	root  *stageNode
	tier  unitTier
	phase *stageNode
	tail  *stageNode
}

func (u liveUnit) started() time.Time {
	if u.phase != nil && !u.phase.started.IsZero() {
		return u.phase.started
	}
	return u.root.started
}

func (u liveUnit) frame() int {
	if u.phase != nil {
		return u.phase.frame
	}
	return u.root.frame
}

func (p *stagePlan) units() []liveUnit {
	active := p.activeSet()
	var out []liveUnit
	for _, id := range p.roots {
		if u, shown := p.unitIfShown(id, active); shown {
			out = append(out, u)
		}
	}
	return out
}

func (p *stagePlan) unitIfShown(id string, active map[string]bool) (liveUnit, bool) {
	root, ok := p.nodes[id]
	if !ok {
		return liveUnit{}, false
	}
	u := liveUnit{root: root, tier: tierRunning}
	shown := false
	p.walk(id, func(n *stageNode) bool {
		if !active[n.id] {
			return true
		}
		switch {
		case n.state == stageDone && n.doneFailed:
			u.tier = tierFailed
			shown = true
		case n.state == stageActive:
			shown = true
			if u.phase == nil && n.linkedParent == id {
				u.phase = n
			}
		}
		return true
	})
	if !shown || u.tier == tierFailed {
		u.phase = nil
		return u, shown
	}
	u.tail = p.tailOf(u, active)
	return u, true
}

func (p *stagePlan) tailOf(u liveUnit, active map[string]bool) *stageNode {
	from := u.root
	if u.phase != nil {
		from = u.phase
	}
	tail := from
	p.walk(from.id, func(n *stageNode) bool {
		if active[n.id] && n.state == stageActive {
			tail = n
		}
		return true
	})
	return tail
}
