package deployui

import (
	"encoding/hex"
	"time"

	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
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
	maxActiveRows     = 20
	maxTreeDepth      = 64
)

type stageNode struct {
	id, parentID string
	title        string
	children     []string
	linked       bool

	state   stageState
	message string
	current uint32
	total   *uint32
	started time.Time
	frame   int
}

type stagePlan struct {
	nodes       map[string]*stageNode
	roots       []string
	orphans     map[string][]string
	final       bool
	activeOrder []string

	droppedNodes   int
	droppedOrphans int
	droppedActive  int
}

func newStagePlan() *stagePlan {
	return &stagePlan{
		nodes:   make(map[string]*stageNode),
		orphans: make(map[string][]string),
	}
}

func stageKey(id []byte) string {
	if len(id) == 0 {
		return ""
	}
	return hex.EncodeToString(id)
}

func (p *stagePlan) apply(ev *deploymentsv1.StagePlanEvent) {
	for _, s := range ev.GetStages() {
		p.declare(s)
	}
	if ev.GetFinal() {
		p.final = true
	}
}

func (p *stagePlan) declare(s *deploymentsv1.Stage) {
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
		n.started = time.Now()
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
	if n.total != nil && current >= *n.total {
		n.state = stageDone
	}
	return n, tracked
}

func (p *stagePlan) ensureActive(id string) {
	for _, existing := range p.activeOrder {
		if existing == id {
			return
		}
	}
	if len(p.activeOrder) >= maxActiveRows {
		p.droppedActive++
		return
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

func (p *stagePlan) siblingPosition(id string) (index, count int, ok bool) {
	n, exists := p.nodes[id]
	if !exists || !n.linked {
		return 0, 0, false
	}
	list := p.roots
	if n.parentID != "" {
		parent, pok := p.nodes[n.parentID]
		if !pok {
			return 0, 0, false
		}
		list = parent.children
	}
	for i, sibling := range list {
		if sibling == id {
			return i + 1, len(list), true
		}
	}
	return 0, 0, false
}
