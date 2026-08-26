package runui

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/fatih/color"
)

type Format string

const (
	Live   Format = "live"
	Plain  Format = "plain"
	NDJSON Format = "json"
)

const (
	okMark    = "✓"
	failMark  = "✗"
	warnMark  = "⚠"
	barWidth  = 12
	frameRate = 80 * time.Millisecond
	bodyPad   = "    "
	pathSep   = " › "
)

var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

type Config struct {
	Format  Format
	Color   bool
	Width   int
	Height  int
	MaxRows int
}

type Renderer struct {
	w   io.Writer
	cfg Config

	mu        sync.Mutex
	tree      *tree
	liveLines int
	start     time.Time
}

func New(w io.Writer, cfg Config) *Renderer {
	if cfg.MaxRows == 0 {
		cfg.MaxRows = 20
	}
	if cfg.Width == 0 {
		cfg.Width = 100
	}
	if cfg.Height == 0 {
		cfg.Height = 40
	}
	return &Renderer{w: w, cfg: cfg, tree: newTree(time.Now), start: time.Now()}
}

func (r *Renderer) Start() {
	if r.cfg.Format != Live {
		return
	}
	go r.tick()
}

func (r *Renderer) tick() {
	t := time.NewTicker(frameRate)
	defer t.Stop()
	for range t.C {
		r.mu.Lock()
		for _, n := range r.tree.nodes {
			if n.state == active {
				n.frame++
			}
		}
		r.erase()
		r.draw()
		r.mu.Unlock()
	}
}

func (r *Renderer) Emit(env Envelope) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.cfg.Format == NDJSON {
		r.emitJSON(env)
		return
	}

	switch {
	case env.Plan != nil:
		r.erase()
		NewPlanPrinter(r.w, r.cfg.Color).Print(env.Plan)
		r.draw()
	case len(env.Stages) > 0:
		for _, decl := range env.Stages {
			r.tree.declare(decl)
		}
	case env.Progress != nil:
		r.onProgress(env.Progress)
	case env.Log != nil:
		r.tree.log(env.Log)
		r.refresh()
	case env.End != nil:
		r.onEnd(env.End)
	case env.Result != nil:
		r.erase()
		r.result(env.Result)
	}
}

func (r *Renderer) onProgress(p *Progress) {
	n := r.tree.progress(p)
	if phase := r.tree.phaseOf(p.StageID); phase != nil {
		phase.live = r.summary(n)
	}
	r.refresh()
}

func (r *Renderer) onEnd(e *StageEnd) {
	n := r.tree.end(e)
	if n == nil {
		return
	}
	switch {
	case n.depth > depthPhase:
		r.tree.record(n.id, r.detailRow(n))
		r.refresh()
	case n.depth == depthPhase:
		r.erase()
		r.flush(n)
		r.draw()
	default:
		r.refresh()
	}
}

func (r *Renderer) flush(phase *node) {
	unit := r.tree.unitOf(phase.id)
	name := phase.title
	if unit != nil && unit != phase && len(unit.children) > 1 {
		name = unit.title + pathSep + phase.title
	} else if unit != nil {
		name = unit.title
	}

	mark, tone := okMark, r.paint(color.FgGreen, color.Bold)
	note := dur(phase.dur)
	switch {
	case phase.failed:
		mark, tone, note = failMark, r.paint(color.FgRed, color.Bold), "failed after "+dur(phase.dur)
	case r.tree.failedDescendants(phase.id) > 0:
		mark, tone = warnMark, r.paint(color.FgYellow, color.Bold)
	}

	if len(phase.body) == 0 && phase.hasBar && phase.message != "" {
		phase.body = append(phase.body, fmt.Sprintf("%s %s  %d/%d",
			r.paint(color.FgGreen).Sprint(okMark), phase.message, phase.total, phase.total))
	}

	fmt.Fprintln(r.w)
	fmt.Fprintf(r.w, "%s %s  %s\n", tone.Sprint(mark), tone.Sprint(name), r.paint(color.Faint).Sprint(note))
	for _, line := range phase.body {
		fmt.Fprintf(r.w, "%s%s\n", bodyPad, line)
	}
}

func (r *Renderer) detailRow(n *node) string {
	if n.failed {
		return r.paint(color.FgRed).Sprintf("%s %s failed", failMark, n.title)
	}
	note := dur(n.dur)
	if n.cached {
		note += " CACHED"
	}
	return fmt.Sprintf("%s %s  %s",
		r.paint(color.FgGreen).Sprint(okMark),
		n.title,
		r.paint(color.Faint).Sprint(note))
}

func (r *Renderer) summary(n *node) string {
	var b strings.Builder
	if n.depth > depthPhase {
		b.WriteString(n.title)
	}
	switch {
	case n.cached:
		join(&b, r.paint(color.Faint).Sprint("CACHED"))
	case n.hasBar && n.total > 0:
		join(&b, fmt.Sprintf("%s %d/%d", bar(n.current, n.total), n.current, n.total))
	case n.message != "" && n.message != n.title:
		join(&b, n.message)
	}
	if b.Len() == 0 && n.depth <= depthPhase {
		return ""
	}
	return b.String()
}

func join(b *strings.Builder, s string) {
	if b.Len() > 0 {
		b.WriteString("  ")
	}
	b.WriteString(s)
}

func (r *Renderer) refresh() {
	if r.cfg.Format != Live {
		return
	}
	r.erase()
	r.draw()
}

func (r *Renderer) erase() {
	if r.cfg.Format != Live || r.liveLines == 0 {
		return
	}
	fmt.Fprintf(r.w, "\033[%dA\033[J", r.liveLines)
	r.liveLines = 0
}

func (r *Renderer) draw() {
	if r.cfg.Format != Live {
		return
	}
	rows := r.tree.live()
	budget := r.cfg.MaxRows
	if room := r.cfg.Height - 4; room < budget {
		budget = room
	}

	lines := 0
	for _, row := range rows {
		if lines+2 > budget {
			fmt.Fprintln(r.w, r.paint(color.Faint).Sprintf("  +%d more", len(rows)-lines/2))
			lines++
			break
		}
		fmt.Fprintln(r.w, truncate(r.unitLine(row), r.cfg.Width))
		lines++
		if tail := r.tailLine(row); tail != "" {
			fmt.Fprintln(r.w, truncate(tail, r.cfg.Width))
			lines++
		}
	}
	r.liveLines = lines
}

func (r *Renderer) unitLine(row unitRow) string {
	n := row.unit
	glyph := r.paint(color.FgCyan).Sprint(spinnerFrames[n.frame%len(spinnerFrames)])
	name := n.title
	if row.phase != nil && len(n.children) > 1 {
		name += r.paint(color.Faint).Sprint(pathSep) + row.phase.title
	}
	started := n.started
	if row.phase != nil {
		started = row.phase.started
	}
	return fmt.Sprintf("%s %s  %s", glyph, r.paint(color.Bold).Sprint(name),
		r.paint(color.Faint).Sprint(dur(time.Since(started))))
}

func (r *Renderer) tailLine(row unitRow) string {
	if row.phase == nil || row.phase.live == "" {
		return ""
	}
	return bodyPad + r.paint(color.Faint).Sprint(truncate(row.phase.live, r.cfg.Width-len(bodyPad)))
}

func (r *Renderer) result(res *Result) {
	fmt.Fprintln(r.w)
	if res.Success {
		r.paint(color.FgGreen, color.Bold).Fprintf(r.w, "%s %s in %s\n", okMark, res.Headline, dur(time.Since(r.start)))
	} else {
		r.paint(color.FgRed, color.Bold).Fprintf(r.w, "%s %s\n", failMark, res.Headline)
		for _, line := range strings.Split(strings.TrimRight(res.Error, "\n"), "\n") {
			if line != "" {
				fmt.Fprintf(r.w, "  %s\n", line)
			}
		}
	}
	if res.Withheld != "" {
		fmt.Fprintln(r.w)
		r.paint(color.FgYellow).Fprintf(r.w, "  %s %s\n", warnMark, res.Withheld)
	}
	if len(res.AppURLs) > 0 {
		fmt.Fprintln(r.w)
		for _, u := range res.AppURLs {
			r.paint(color.FgCyan, color.Bold).Fprintf(r.w, "  %s\n", u)
		}
	}
	for _, d := range res.Diagnostic {
		r.paint(color.Faint).Fprintf(r.w, "  %s\n", d)
	}
	if res.StreamAt != "" {
		fmt.Fprintln(r.w)
		r.paint(color.Faint).Fprintf(r.w, "  Stream: %s\n", res.StreamAt)
	}
}

func (r *Renderer) emitJSON(env Envelope) {
	rec := map[string]any{}
	switch {
	case env.Plan != nil:
		rec["plan"] = planJSON(env.Plan)
	case len(env.Stages) > 0:
		stages := make([]map[string]any, 0, len(env.Stages))
		for _, s := range env.Stages {
			entry := map[string]any{"id": s.ID, "title": s.Title}
			if s.Parent != "" {
				entry["parentId"] = s.Parent
			}
			stages = append(stages, entry)
		}
		rec["stagePlan"] = map[string]any{"stages": stages}
	case env.Progress != nil:
		p := map[string]any{"stageId": env.Progress.StageID, "message": env.Progress.Message}
		if env.Progress.HasBar {
			p["current"] = env.Progress.Current
			p["total"] = env.Progress.Total
		}
		if env.Progress.Cached {
			p["cached"] = true
		}
		rec["progress"] = p
	case env.Log != nil:
		rec["log"] = map[string]any{"stageId": env.Log.StageID, "message": collapseCarriage(env.Log.Line)}
	case env.End != nil:
		rec["spanEnd"] = map[string]any{"stageId": env.End.StageID, "failed": env.End.Failed}
	case env.Result != nil:
		res := map[string]any{"success": env.Result.Success, "headline": env.Result.Headline}
		if env.Result.Error != "" {
			res["error"] = env.Result.Error
		}
		if len(env.Result.AppURLs) > 0 {
			res["appUrls"] = env.Result.AppURLs
		}
		if env.Result.Withheld != "" {
			res["withheld"] = env.Result.Withheld
		}
		rec["result"] = res
	default:
		return
	}
	raw, err := json.Marshal(rec)
	if err != nil {
		return
	}
	fmt.Fprintln(r.w, string(raw))
}

func planJSON(plan *Plan) map[string]any {
	groups := make([]map[string]any, 0, len(plan.Groups))
	for _, g := range plan.Groups {
		rows := make([]map[string]any, 0, len(g.Rows))
		for _, row := range g.Rows {
			entry := map[string]any{"name": row.Name, "action": actionName(row.Action)}
			if row.Kind != "" {
				entry["kind"] = row.Kind
			}
			if row.Reason != "" {
				entry["reason"] = row.Reason
			}
			rows = append(rows, entry)
		}
		group := map[string]any{"name": g.Name, "changes": rows}
		if g.Kind != "" {
			group["kind"] = g.Kind
		}
		groups = append(groups, group)
	}
	out := map[string]any{"subject": plan.Subject, "groups": groups}
	if plan.EdgeKind != "" {
		out["edgeKind"] = plan.EdgeKind
	}
	return out
}

func actionName(a Action) string {
	switch a {
	case Create:
		return "ACTION_CREATE"
	case Update:
		return "ACTION_UPDATE"
	case Replace:
		return "ACTION_REPLACE"
	case Delete:
		return "ACTION_DELETE"
	case DisableThenDelete:
		return "ACTION_DISABLE_THEN_DELETE"
	default:
		return "ACTION_KEEP"
	}
}

func (r *Renderer) paint(attrs ...color.Attribute) *color.Color {
	c := color.New(attrs...)
	if r.cfg.Color {
		c.EnableColor()
	} else {
		c.DisableColor()
	}
	return c
}

func bar(current, total uint32) string {
	if total == 0 {
		return ""
	}
	filled := int(float64(current) / float64(total) * barWidth)
	if filled > barWidth {
		filled = barWidth
	}
	return "[" + strings.Repeat("█", filled) + strings.Repeat("░", barWidth-filled) + "]"
}

func dur(d time.Duration) string {
	switch {
	case d <= 0:
		return "0s"
	case d < 500*time.Millisecond:
		return "<1s"
	}
	rounded := d.Round(time.Second)
	if rounded < time.Minute {
		return fmt.Sprintf("%ds", int(rounded/time.Second))
	}
	return fmt.Sprintf("%dm%02ds", int(rounded/time.Minute), int((rounded%time.Minute)/time.Second))
}

func truncate(s string, width int) string {
	limit := width - 1
	if limit < 1 {
		limit = 1
	}
	var b strings.Builder
	visible, escape := 0, false
	for _, r := range s {
		switch {
		case escape:
			b.WriteRune(r)
			if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') {
				escape = false
			}
			continue
		case r == '\x1b':
			b.WriteRune(r)
			escape = true
			continue
		}
		if visible == limit {
			return b.String() + "…\x1b[0m"
		}
		b.WriteRune(r)
		visible++
	}
	return s
}
