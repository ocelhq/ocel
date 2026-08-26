package runui

import (
	"strings"
	"time"
)

type nodeState int

const (
	pending nodeState = iota
	active
	done
)

const (
	depthUnit = iota
	depthPhase
)

type node struct {
	id, parent string
	title      string
	children   []string
	depth      int

	state   nodeState
	message string
	current uint32
	total   uint32
	hasBar  bool
	cached  bool

	body []string
	live string

	started time.Time
	dur     time.Duration
	failed  bool
	frame   int
}

type tree struct {
	nodes  map[string]*node
	roots  []string
	orphan map[string][]string
	now    func() time.Time
}

func newTree(now func() time.Time) *tree {
	return &tree{
		nodes:  map[string]*node{},
		orphan: map[string][]string{},
		now:    now,
	}
}

func (t *tree) declare(d StageDecl) {
	if _, ok := t.nodes[d.ID]; ok {
		return
	}
	n := &node{id: d.ID, parent: d.Parent, title: d.Title}
	t.nodes[d.ID] = n
	t.link(n)
}

func (t *tree) link(n *node) {
	if n.parent == "" {
		n.depth = depthUnit
		t.roots = append(t.roots, n.id)
		t.adopt(n.id)
		return
	}
	parent, ok := t.nodes[n.parent]
	if !ok {
		t.orphan[n.parent] = append(t.orphan[n.parent], n.id)
		return
	}
	n.depth = parent.depth + 1
	parent.children = append(parent.children, n.id)
	t.adopt(n.id)
}

func (t *tree) adopt(id string) {
	waiting := t.orphan[id]
	delete(t.orphan, id)
	for _, child := range waiting {
		if n, ok := t.nodes[child]; ok {
			t.link(n)
		}
	}
}

func (t *tree) phaseOf(id string) *node {
	n := t.nodes[id]
	for n != nil && n.depth > depthPhase {
		n = t.nodes[n.parent]
	}
	if n == nil || n.depth != depthPhase {
		return nil
	}
	return n
}

func (t *tree) unitOf(id string) *node {
	n := t.nodes[id]
	for n != nil && n.depth > depthUnit {
		n = t.nodes[n.parent]
	}
	return n
}

func (t *tree) progress(p *Progress) *node {
	n, ok := t.nodes[p.StageID]
	if !ok {
		t.declare(StageDecl{ID: p.StageID, Title: p.Message})
		n = t.nodes[p.StageID]
	}
	if n.state != active {
		n.started = t.now()
		n.state = active
	}
	n.message = p.Message
	n.current = p.Current
	n.total = p.Total
	n.hasBar = p.HasBar
	if p.Cached {
		n.cached = true
	}
	return n
}

func collapseCarriage(line string) string {
	if i := strings.LastIndex(line, "\r"); i >= 0 {
		return line[i+1:]
	}
	return line
}

func (t *tree) log(l *Log) *node {
	phase := t.phaseOf(l.StageID)
	if phase == nil {
		return nil
	}
	line := collapseCarriage(l.Line)
	phase.body = append(phase.body, line)
	if strings.TrimSpace(line) != "" {
		phase.live = line
	}
	return phase
}

func (t *tree) record(id, line string) {
	if phase := t.phaseOf(id); phase != nil && phase.id != id {
		phase.body = append(phase.body, line)
	}
}

func (t *tree) end(e *StageEnd) *node {
	n, ok := t.nodes[e.StageID]
	if !ok {
		return nil
	}
	if n.state == done {
		return n
	}
	n.state = done
	n.failed = e.Failed
	n.dur = t.now().Sub(n.started)
	return n
}

func (t *tree) failedDescendants(id string) int {
	n, ok := t.nodes[id]
	if !ok {
		return 0
	}
	lost := 0
	for _, child := range n.children {
		c := t.nodes[child]
		if c == nil {
			continue
		}
		if c.failed || t.failedDescendants(child) > 0 {
			lost++
		}
	}
	return lost
}

func (t *tree) activePhase(unitID string) *node {
	n, ok := t.nodes[unitID]
	if !ok {
		return nil
	}
	for _, child := range n.children {
		if c := t.nodes[child]; c != nil && c.state == active {
			return c
		}
	}
	return nil
}

type unitRow struct {
	unit  *node
	phase *node
}

func (t *tree) live() []unitRow {
	var out []unitRow
	for _, id := range t.roots {
		u := t.nodes[id]
		if u == nil || u.state == done {
			continue
		}
		phase := t.activePhase(id)
		if phase == nil && u.state != active {
			continue
		}
		out = append(out, unitRow{unit: u, phase: phase})
	}
	return out
}
