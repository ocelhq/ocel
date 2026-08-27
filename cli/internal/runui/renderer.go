package runui

import (
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/fatih/color"

	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
)

const (
	okMark    = "✓"
	failMark  = "✗"
	warnMark  = "⚠"
	startMark = "→"
	barWidth  = 12
)

type Renderer struct {
	w       io.Writer
	present Presentation

	mu        sync.Mutex
	plan      *stagePlan
	liveLines int
	waiting   bool
	spinning  bool
	spinMsg   string
	spinFrame int

	tickStop chan struct{}
	tickDone chan struct{}
}

var liveWriters sync.Map

func rendererFor(w io.Writer) (*Renderer, bool) {
	v, ok := liveWriters.Load(w)
	if !ok {
		return nil, false
	}
	r, ok := v.(*Renderer)
	return r, ok
}

func NewRenderer(w io.Writer, present Presentation) *Renderer {
	r := &Renderer{
		w:       w,
		present: present,
		plan:    newStagePlan(),
	}
	liveWriters.Store(w, r)
	if r.present.Live() {
		r.tickStop = make(chan struct{})
		r.tickDone = make(chan struct{})
		go r.tickLoop()
	}
	return r
}

func (r *Renderer) Live() bool { return r.present.Live() }

func (r *Renderer) width() int {
	if n, ok := liveWidth(r.w); ok {
		return n
	}
	return r.present.Width
}

func (r *Renderer) useClock(now func() time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.plan.useClock(now)
}

func (r *Renderer) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.present.Live() && !r.waiting {
		r.eraseLiveLocked()
		n, err := r.w.Write(p)
		r.drawLiveLocked()
		return n, err
	}
	return r.w.Write(p)
}

func (r *Renderer) Commit(lines []string) {
	if len(lines) == 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.eraseLiveLocked()
	for _, line := range lines {
		fmt.Fprintln(r.w, r.paint(line))
	}
	r.drawLiveLocked()
}

func (r *Renderer) paint(line string) string {
	mark, rest, ok := strings.Cut(line, " ")
	if !ok {
		return line
	}
	switch mark {
	case okMark:
		return r.colorFor(color.FgGreen).Sprint(mark) + " " + rest
	case failMark:
		return r.colorFor(color.FgRed, color.Bold).Sprint(mark + " " + rest)
	case warnMark:
		return r.colorFor(color.FgYellow, color.Bold).Sprint(mark) + " " + rest
	case startMark:
		return r.colorFor(color.Faint).Sprint(mark + " " + rest)
	default:
		return line
	}
}

func (r *Renderer) Ingest(ev *streamv1.RunEvent) {
	op := ev.GetOperation()
	if op == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	switch {
	case op.GetStagePlan() != nil:
		r.plan.apply(op.GetStagePlan())
	case op.GetProgress() != nil:
		p := op.GetProgress()
		r.trackLocked(stageKey(p.GetStageId()), p.GetMessage(), p.GetCurrent(), p.Total)
	case op.GetSpan() != nil:
		r.endLocked(op.GetSpan())
	default:
		return
	}
	r.eraseLiveLocked()
	r.drawLiveLocked()
}

func (r *Renderer) trackLocked(id, message string, current uint32, total *uint32) {
	if id == "" {
		return
	}
	if n, ok := r.plan.nodes[id]; ok && n.state == stageDone {
		return
	}
	n, tracked := r.plan.progress(id, message, current, total)
	if n.title == "" {
		n.title = message
	}
	if tracked {
		r.plan.ensureActive(id)
	}
}

func (r *Renderer) endLocked(span *progressv1.SpanEvent) {
	id := stageKey(span.GetSpanId())
	n, ok := r.plan.nodes[id]
	if !ok {
		return
	}
	n.state = stageDone
	n.doneFailed = span.GetStatus() == progressv1.SpanStatus_SPAN_STATUS_ERROR
	n.doneDur = spanDuration(span)
	if r.plan.hasActiveAncestor(id) {
		return
	}
	for _, row := range r.plan.subtreeRows(id) {
		r.plan.removeActive(row.n.id)
	}
	r.plan.removeActive(id)
}

func (r *Renderer) Restart(stageID []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.plan.restart(stageKey(stageID))
}

func (r *Renderer) tickLoop() {
	defer close(r.tickDone)
	t := time.NewTicker(frameRate)
	defer t.Stop()
	for {
		select {
		case <-r.tickStop:
			return
		case <-t.C:
			r.mu.Lock()
			if !r.waiting && (len(r.plan.activeOrder) > 0 || r.spinning) {
				for _, id := range r.plan.activeOrder {
					if n := r.plan.nodes[id]; n != nil {
						n.frame++
					}
				}
				if r.spinning {
					r.spinFrame++
				}
				r.eraseLiveLocked()
				r.drawLiveLocked()
			}
			r.mu.Unlock()
		}
	}
}

func (r *Renderer) Close() error {
	if r.tickStop != nil {
		close(r.tickStop)
		<-r.tickDone
		r.tickStop = nil
	}
	r.mu.Lock()
	r.eraseLiveLocked()
	r.mu.Unlock()
	liveWriters.Delete(r.w)
	return nil
}

func (r *Renderer) Suspend() func() {
	r.mu.Lock()
	if !r.present.Live() || r.waiting {
		r.mu.Unlock()
		return func() {}
	}
	r.eraseLiveLocked()
	r.waiting = true
	r.mu.Unlock()

	return func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.waiting = false
		r.drawLiveLocked()
	}
}

func (r *Renderer) Pause() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.eraseLiveLocked()
	r.waiting = true
}

func (r *Renderer) Resume() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.waiting = false
	r.drawLiveLocked()
}

func (r *Renderer) Spin(msg string) func() {
	r.mu.Lock()
	if !r.present.Live() || r.waiting {
		r.mu.Unlock()
		return func() {}
	}
	r.eraseLiveLocked()
	r.spinning = true
	r.spinMsg = msg
	r.spinFrame = 0
	r.drawLiveLocked()
	r.mu.Unlock()

	return func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		if !r.spinning {
			return
		}
		r.eraseLiveLocked()
		r.spinning = false
		r.spinMsg = ""
		r.drawLiveLocked()
	}
}

func (r *Renderer) eraseLiveLocked() {
	if r.liveLines == 0 {
		return
	}
	fmt.Fprintf(r.w, "\033[%dA\033[J", r.liveLines)
	r.liveLines = 0
}

func (r *Renderer) drawLiveLocked() {
	if !r.present.Live() || r.waiting {
		return
	}
	width := r.width()
	maxRows := r.effectiveMaxRowsLocked()

	shown := r.plan.displayRows()
	overflow := r.plan.droppedActive
	if len(shown) > maxRows {
		overflow += len(shown) - maxRows
		shown = shown[:maxRows]
	}

	lines := 0
	for _, row := range shown {
		fmt.Fprintln(r.w, truncateToWidth(r.rowLineLocked(row), width))
		lines++
	}
	if overflow > 0 {
		fmt.Fprintln(r.w, truncateToWidth(r.colorFor(color.Faint).Sprintf("  … and %d more", overflow), width))
		lines++
	}
	if r.spinning {
		fmt.Fprintln(r.w, truncateToWidth(r.spinRowLocked(), width))
		lines++
	}
	r.liveLines = lines
}

func (r *Renderer) effectiveMaxRowsLocked() int {
	limit := maxActiveRows
	if budget := termHeight(r.w) - 3; budget < limit {
		if budget < 1 {
			budget = 1
		}
		limit = budget
	}
	return limit
}

func (r *Renderer) spinRowLocked() string {
	glyph := r.colorFor(color.FgCyan).Sprint(spinnerFrame(r.spinFrame))
	return fmt.Sprintf("%s %s", glyph, r.spinMsg)
}

func (r *Renderer) rowLineLocked(row displayRow) string {
	n := row.n
	indent := strings.Repeat("  ", row.depth)
	if n.state == stageDone {
		if n.doneFailed {
			return indent + r.colorFor(color.FgRed, color.Bold).Sprintf("%s %s failed", failMark, n.title)
		}
		return fmt.Sprintf("%s%s  %s",
			indent,
			r.colorFor(color.FgGreen).Sprintf("%s %s", okMark, n.title),
			r.colorFor(color.Faint).Sprint(formatDuration(n.doneDur)))
	}
	glyph := r.colorFor(color.FgCyan).Sprint(spinnerFrame(n.frame))
	var b strings.Builder
	fmt.Fprintf(&b, "%s%s %s", indent, glyph, n.title)
	fmt.Fprintf(&b, "  %s", r.colorFor(color.Faint).Sprint(formatDuration(r.plan.now().Sub(n.started))))
	switch {
	case n.total != nil:
		fmt.Fprintf(&b, "  %s %d/%d", bar(n.current, *n.total), n.current, *n.total)
	case n.message != "" && n.message != n.title:
		fmt.Fprintf(&b, "  %s", r.colorFor(color.Faint).Sprintf("— %s", n.message))
	}
	return b.String()
}

func (r *Renderer) colorFor(attrs ...color.Attribute) *color.Color {
	c := color.New(attrs...)
	if r.present.Color {
		c.EnableColor()
	} else {
		c.DisableColor()
	}
	return c
}

func phaseLabel(p progressv1.Phase) string {
	switch p {
	case progressv1.Phase_PHASE_BUILDING:
		return "Building"
	case progressv1.Phase_PHASE_UPLOADING:
		return "Uploading"
	case progressv1.Phase_PHASE_PROVISIONING:
		return "Provisioning"
	case progressv1.Phase_PHASE_FINALIZING:
		return "Finalizing"
	case progressv1.Phase_PHASE_DELETING:
		return "Deleting"
	default:
		return "Working"
	}
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

func formatDuration(d time.Duration) string {
	if d <= 0 {
		return "0s"
	}
	if d < 500*time.Millisecond {
		return "<1s"
	}
	rounded := d.Round(time.Second)
	if rounded < time.Minute {
		return fmt.Sprintf("%ds", int(rounded/time.Second))
	}
	m := int(rounded / time.Minute)
	sec := int((rounded % time.Minute) / time.Second)
	return fmt.Sprintf("%dm%02ds", m, sec)
}
