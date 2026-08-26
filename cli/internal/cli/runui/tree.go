package runui

import "time"

type nodeState int

const (
	pending nodeState = iota
	active
	done
)

type node struct {
	id, parent string
	title      string
	children   []string

	state   nodeState
	message string
	current uint32
	total   uint32
	hasBar  bool
	cached  bool
	logs    []string

	started time.Time
	dur     time.Duration
	failed  bool
	frame   int
}

type tree struct {
	nodes  map[string]*node
	roots  []string
	order  []string
	tailN  int
	now    func() time.Time
	orphan map[string][]string
}

func newTree(tailN int, now func() time.Time) *tree {
	return &tree{
		nodes:  map[string]*node{},
		tailN:  tailN,
		now:    now,
		orphan: map[string][]string{},
	}
}

func (t *tree) declare(d StageDecl) {
	if _, ok := t.nodes[d.ID]; ok {
		return
	}
	n := &node{id: d.ID, parent: d.Parent, title: d.Title}
	t.nodes[d.ID] = n
	t.order = append(t.order, d.ID)
	t.link(n)
}

func (t *tree) link(n *node) {
	if n.parent == "" {
		t.roots = append(t.roots, n.id)
		t.adopt(n.id)
		return
	}
	parent, ok := t.nodes[n.parent]
	if !ok {
		t.orphan[n.parent] = append(t.orphan[n.parent], n.id)
		return
	}
	parent.children = append(parent.children, n.id)
	t.adopt(n.id)
}

func (t *tree) adopt(id string) {
	pending := t.orphan[id]
	delete(t.orphan, id)
	for _, child := range pending {
		if n, ok := t.nodes[child]; ok {
			t.link(n)
		}
	}
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

func (t *tree) log(l *Log) {
	n, ok := t.nodes[l.StageID]
	if !ok {
		return
	}
	n.logs = append(n.logs, l.Line)
	if len(n.logs) > 200 {
		n.logs = n.logs[len(n.logs)-200:]
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
		if c.failed {
			lost++
			continue
		}
		if t.failedDescendants(child) > 0 {
			lost++
		}
	}
	return lost
}

func (t *tree) siblings(id string) (index, count int) {
	n, ok := t.nodes[id]
	if !ok {
		return 0, 0
	}
	list := t.roots
	if n.parent != "" {
		parent, ok := t.nodes[n.parent]
		if !ok {
			return 0, 0
		}
		list = parent.children
	}
	for i, sib := range list {
		if sib == id {
			return i + 1, len(list)
		}
	}
	return 0, 0
}

func (t *tree) hasActiveDescendant(id string) bool {
	n, ok := t.nodes[id]
	if !ok {
		return false
	}
	for _, child := range n.children {
		c := t.nodes[child]
		if c == nil {
			continue
		}
		if c.state == active || t.hasActiveDescendant(child) {
			return true
		}
	}
	return false
}

type line struct {
	n      *node
	depth  int
	tail   string
	extra  int
	isTail bool
}

func (t *tree) frame(budget int) []line {
	if budget < 1 {
		budget = 1
	}
	live := make([]string, 0, len(t.roots))
	for _, id := range t.roots {
		if t.subtreeActive(id) {
			live = append(live, id)
		}
	}
	if len(live) == 0 {
		return nil
	}

	share := budget / len(live)
	if share < 1 {
		share = 1
	}
	spare := budget - share*len(live)

	var out []line
	for _, id := range live {
		allowance := share
		if spare > 0 {
			allowance++
			spare--
		}
		rows, dropped := t.subtree(id, 0, allowance)
		out = append(out, rows...)
		if dropped > 0 {
			out = append(out, line{depth: 1, extra: dropped})
		}
	}
	return out
}

func (t *tree) subtreeActive(id string) bool {
	n, ok := t.nodes[id]
	if !ok {
		return false
	}
	return n.state == active || t.hasActiveDescendant(id)
}

func (t *tree) subtree(id string, depth, allowance int) (rows []line, dropped int) {
	n, ok := t.nodes[id]
	if !ok {
		return nil, 0
	}
	rows = append(rows, line{n: n, depth: depth})
	allowance--

	kids := make([]string, 0, len(n.children))
	for _, child := range n.children {
		if t.subtreeActive(child) {
			kids = append(kids, child)
		}
	}

	if len(kids) == 0 {
		for _, tail := range t.tail(n) {
			if allowance <= 0 {
				dropped++
				continue
			}
			rows = append(rows, line{n: n, depth: depth + 1, tail: tail, isTail: true})
			allowance--
		}
		return rows, dropped
	}

	for _, child := range kids {
		if allowance <= 0 {
			dropped += t.weight(child)
			continue
		}
		sub, subDropped := t.subtree(child, depth+1, allowance)
		rows = append(rows, sub...)
		allowance -= len(sub)
		dropped += subDropped
	}
	return rows, dropped
}

func (t *tree) weight(id string) int {
	n, ok := t.nodes[id]
	if !ok {
		return 0
	}
	total := 1
	for _, child := range n.children {
		if t.subtreeActive(child) {
			total += t.weight(child)
		}
	}
	return total
}

func (t *tree) tail(n *node) []string {
	if t.tailN <= 0 || len(n.logs) == 0 || n.state != active {
		return nil
	}
	if len(n.logs) <= t.tailN {
		return n.logs
	}
	return n.logs[len(n.logs)-t.tailN:]
}
