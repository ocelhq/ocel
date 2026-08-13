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

// stageNode is one node of the tree a StagePlanEvent grows. Its id is the
// provider's span id, hex-encoded so it can key a map.
type stageNode struct {
	id, parentID string
	title        string
	children     []string
	linked       bool // already attached under its parent (or as a root)

	state   stageState
	message string
	current uint32
	total   *uint32
	started time.Time
	frame   int
}

// stagePlan is the tree a sequence of StagePlanEvents grows, plus the
// progress state layered onto it. Stages can arrive out of order — a child
// naming a parent_id not yet seen is buffered and attached once its parent
// arrives.
type stagePlan struct {
	nodes       map[string]*stageNode
	roots       []string
	orphans     map[string][]string // parent id -> buffered child ids
	final       bool
	activeOrder []string // stage ids currently in the live region, in the order they started
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
	n := p.nodeFor(id)
	n.title = s.GetTitle()
	n.parentID = stageKey(s.GetParentId())
	p.link(n)
}

func (p *stagePlan) nodeFor(id string) *stageNode {
	n, ok := p.nodes[id]
	if !ok {
		n = &stageNode{id: id}
		p.nodes[id] = n
	}
	return n
}

func (p *stagePlan) link(n *stageNode) {
	if n.linked {
		return
	}
	if n.parentID == "" {
		p.roots = append(p.roots, n.id)
		n.linked = true
		p.attachOrphans(n.id)
		return
	}
	parent, ok := p.nodes[n.parentID]
	if !ok {
		p.orphans[n.parentID] = append(p.orphans[n.parentID], n.id)
		return
	}
	parent.children = append(parent.children, n.id)
	n.linked = true
	p.attachOrphans(n.id)
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

// progress records a ProgressEvent against the stage it names, creating the
// node if its Stage declaration has not arrived yet (defensive: a plan and
// its progress travel on the same stream, but nothing enforces the order).
// It reports whether this is the stage's first progress event.
func (p *stagePlan) progress(id, message string, current uint32, total *uint32) (n *stageNode, started bool) {
	n = p.nodeFor(id)
	started = n.state == stagePending
	if started {
		n.state = stageActive
		n.started = time.Now()
	}
	n.message = message
	n.current = current
	n.total = total
	if total != nil && current >= *total {
		n.state = stageDone
	}
	return n, started
}

func (n *stageNode) leaf() bool {
	return len(n.children) == 0
}

func (p *stagePlan) removeActive(id string) {
	for i, existing := range p.activeOrder {
		if existing == id {
			p.activeOrder = append(p.activeOrder[:i], p.activeOrder[i+1:]...)
			return
		}
	}
}
