package deployui

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/fatih/color"

	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
)

const (
	okMark   = "✓"
	failMark = "✗"
	warnMark = "⚠"
	barWidth = 12
)

var buildStageID = []byte("build")

const untaggedStageID = "untagged"

type Format string

const (
	FormatHuman Format = "human"
	FormatJSON  Format = "json"
)

type Renderer struct {
	w       io.Writer
	format  Format
	live    bool
	color   bool
	verbose bool

	mu        sync.Mutex
	plan      *stagePlan
	liveLines int
	waiting   bool
	spinning  bool
	spinMsg   string
	spinFrame int
	start     time.Time

	tickStop chan struct{}
	tickDone chan struct{}
}

var liveWriters sync.Map // io.Writer -> *Renderer

func rendererFor(w io.Writer) (*Renderer, bool) {
	v, ok := liveWriters.Load(w)
	if !ok {
		return nil, false
	}
	r, ok := v.(*Renderer)
	return r, ok
}

func NewRenderer(w io.Writer, format Format, verbose bool) *Renderer {
	live := format == FormatHuman && !verbose && IsTerminal(w)
	r := newRendererForTest(w, format, live, IsTerminal(w))
	r.verbose = verbose
	return r
}

func newRendererForTest(w io.Writer, format Format, live, colorEnabled bool) *Renderer {
	r := &Renderer{
		w:      w,
		format: format,
		live:   live,
		color:  colorEnabled,
		plan:   newStagePlan(),
		start:  time.Now(),
	}
	liveWriters.Store(w, r)
	if r.live {
		r.tickStop = make(chan struct{})
		r.tickDone = make(chan struct{})
		go r.tickLoop()
	}
	return r
}

func (r *Renderer) Live() bool { return r.live }

func (r *Renderer) useClock(now func() time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.plan.useClock(now)
}

func (r *Renderer) RestartBuildStage() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.plan.restart(stageKey(buildStageID))
}

func (r *Renderer) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.live && !r.waiting {
		r.eraseLiveLocked()
		n, err := r.w.Write(p)
		r.drawLiveLocked()
		return n, err
	}
	return r.w.Write(p)
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

func (r *Renderer) Spin(msg string) func() {
	r.mu.Lock()
	if !r.live || r.waiting {
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

func (r *Renderer) Progress(stageID []byte, phase deploymentsv1.Phase, message string, current uint32, total *uint32) {
	if r.format == FormatJSON {
		fields := map[string]any{"phase": phaseTag(phase), "message": message}
		if id := stageKey(stageID); id != "" {
			fields["stageId"] = id
		}
		if total != nil {
			fields["current"] = current
			fields["total"] = *total
		}
		r.emitJSON("progress", fields)
		return
	}
	if !r.live {
		r.mu.Lock()
		defer r.mu.Unlock()
		fmt.Fprintln(r.w, message)
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	r.eraseLiveLocked()

	id := stageKey(stageID)
	if id == "" {
		r.progressUntaggedLocked(phase, message, current, total)
	} else {
		r.progressStageLocked(id, phase, message, current, total)
	}

	r.drawLiveLocked()
}

func (r *Renderer) progressStageLocked(id string, phase deploymentsv1.Phase, message string, current uint32, total *uint32) {
	n, tracked := r.plan.progress(id, message, current, total)
	if n.title == "" {
		n.title = fallbackTitle(phase, message)
	}
	if !tracked {
		return
	}
	if n.state == stageDone {
		if r.plan.isActive(id) {
			r.commitLocked(n, r.okColor(), okMark, "")
		}
		return
	}
	r.plan.ensureActive(id)
}

func (r *Renderer) progressUntaggedLocked(phase deploymentsv1.Phase, message string, current uint32, total *uint32) {
	if r.plan.isActive(untaggedStageID) {
		if n := r.plan.nodes[untaggedStageID]; n != nil {
			r.commitLocked(n, r.okColor(), okMark, "")
		}
	}
	n, tracked := r.plan.progress(untaggedStageID, message, current, total)
	n.title = fallbackTitle(phase, message)
	if tracked {
		r.plan.ensureActive(untaggedStageID)
	}
}

func (r *Renderer) StagePlan(ev *deploymentsv1.StagePlanEvent) {
	if r.format == FormatJSON {
		r.emitJSON("stagePlan", map[string]any{"final": ev.GetFinal(), "count": len(ev.GetStages())})
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.plan.apply(ev)
}

func (r *Renderer) Log(message string) {
	if r.format == FormatJSON {
		r.emitJSON("log", map[string]any{"message": message})
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.verbose {
		return
	}
	if r.live {
		r.eraseLiveLocked()
		fmt.Fprintln(r.w, message)
		r.drawLiveLocked()
		return
	}
	fmt.Fprintln(r.w, message)
}

func (r *Renderer) StageEnd(stageID []byte, failed bool, duration time.Duration) {
	if r.format == FormatJSON {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	id := stageKey(stageID)
	n, ok := r.plan.nodes[id]
	if !ok {
		return
	}
	n.state = stageDone
	if !r.plan.isActive(id) {
		return
	}
	r.eraseLiveLocked()
	if failed {
		r.finalizeLineLocked(n, r.colorFor(color.FgRed, color.Bold), failMark, "failed", duration)
	} else {
		r.finalizeLineLocked(n, r.okColor(), okMark, "", duration)
	}
	r.plan.removeActive(id)
	r.drawLiveLocked()
}

func (r *Renderer) Building() {
	r.Progress(buildStageID, deploymentsv1.Phase_PHASE_UNSPECIFIED, "Building project", 0, nil)
}

func (r *Renderer) BuildOK() {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := stageKey(buildStageID)
	n, ok := r.plan.nodes[key]
	if !ok || !r.plan.isActive(key) {
		return
	}
	r.commitLocked(n, r.okColor(), okMark, "")
}

func (r *Renderer) Waiting(reason, url string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.waiting = true
	if r.format == FormatJSON {
		r.emitJSONLocked("waiting", map[string]any{"reason": reason, "url": url})
		return
	}
	r.eraseLiveLocked()
	fmt.Fprintf(r.w, "\n%s\n  Fill them in at:\n\n    %s\n\n  Waiting for the page — press Ctrl-C to abort. Nothing has been provisioned.\n\n",
		reason, url)
}

func (r *Renderer) Resume() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.waiting = false
	r.drawLiveLocked()
}

func (r *Renderer) Deployed(headline string, appURLs []string, logPath string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.finishAllLocked(r.okColor(), okMark, "")

	if r.format == FormatJSON {
		r.emitJSONLocked("deployed", map[string]any{
			"headline":   headline,
			"appUrls":    appURLs,
			"durationMs": time.Since(r.start).Milliseconds(),
			"logPath":    logPath,
		})
		return
	}

	fmt.Fprintln(r.w)
	r.colorFor(color.FgGreen, color.Bold).Fprintf(r.w, "%s %s in %s\n", okMark, headline, formatDuration(time.Since(r.start)))
	if len(appURLs) > 0 {
		fmt.Fprintln(r.w)
		url := r.colorFor(color.FgCyan, color.Bold)
		for _, u := range appURLs {
			url.Fprintf(r.w, "  %s\n", u)
		}
	}
	r.printLogPointerLocked("Details", logPath)
}

func (r *Renderer) Finish(headline, logPath string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.finishAllLocked(r.okColor(), okMark, "")

	if r.format == FormatJSON {
		r.emitJSONLocked("finished", map[string]any{
			"headline":   headline,
			"durationMs": time.Since(r.start).Milliseconds(),
			"logPath":    logPath,
		})
		return
	}
	fmt.Fprintln(r.w)
	r.colorFor(color.FgGreen, color.Bold).Fprintf(r.w, "%s %s (%s)\n", okMark, headline, formatDuration(time.Since(r.start)))
	r.printLogPointerLocked("Details", logPath)
}

func (r *Renderer) Fail(err error, logPath string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	hadRows := r.finishAllLocked(r.colorFor(color.FgRed, color.Bold), failMark, "failed")

	if r.format == FormatJSON {
		r.emitJSONLocked("failed", map[string]any{"error": err.Error(), "logPath": logPath})
		return
	}
	if !hadRows {
		r.colorFor(color.FgRed, color.Bold).Fprintf(r.w, "%s Failed\n", failMark)
	}
	for _, line := range strings.Split(strings.TrimRight(err.Error(), "\n"), "\n") {
		fmt.Fprintf(r.w, "  %s\n", line)
	}
	r.printLogPointerLocked("Full log", logPath)
}

func (r *Renderer) Cancel(command string, waiting bool, logPath string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	hadRows := r.finishAllLocked(r.colorFor(color.FgYellow, color.Bold), warnMark, "cancelled")

	if r.format == FormatJSON {
		r.emitJSONLocked("cancelled", map[string]any{"waiting": waiting, "command": command, "logPath": logPath})
		return
	}
	if !hadRows {
		r.colorFor(color.FgYellow, color.Bold).Fprintf(r.w, "%s Cancelled\n", warnMark)
	}
	if waiting {
		fmt.Fprintln(r.w, "  Nothing has been provisioned.")
	} else {
		fmt.Fprintln(r.w, "  Resources may be partially created.")
	}
	fmt.Fprintf(r.w, "  Re-run `%s` to reconcile.\n", command)
	r.printLogPointerLocked("Log", logPath)
}

func (r *Renderer) printLogPointerLocked(label, logPath string) {
	if logPath == "" {
		return
	}
	fmt.Fprintln(r.w)
	r.colorFor(color.Faint).Fprintf(r.w, "  %s: %s\n", label, relLog(logPath))
}

func (r *Renderer) finishAllLocked(c *color.Color, mark, status string) bool {
	if len(r.plan.activeOrder) == 0 {
		return false
	}
	r.eraseLiveLocked()
	for _, id := range r.plan.activeOrder {
		if n := r.plan.nodes[id]; n != nil {
			r.finalizeLineLocked(n, c, mark, status, r.plan.now().Sub(n.started))
			n.state = stageDone
		}
	}
	r.plan.activeOrder = nil
	return true
}

func (r *Renderer) commitLocked(n *stageNode, c *color.Color, mark, status string) {
	r.finalizeLineLocked(n, c, mark, status, r.plan.now().Sub(n.started))
	r.plan.removeActive(n.id)
}

func (r *Renderer) finalizeLineLocked(n *stageNode, c *color.Color, mark, status string, duration time.Duration) {
	if status == "" {
		c.Fprintf(r.w, "%s %s", mark, n.title)
		r.colorFor(color.Faint).Fprintf(r.w, "  %s\n", formatDuration(duration))
		return
	}
	c.Fprintf(r.w, "%s %s %s\n", mark, n.title, status)
}

func (r *Renderer) eraseLiveLocked() {
	if r.liveLines == 0 {
		return
	}
	fmt.Fprintf(r.w, "\033[%dA\033[J", r.liveLines)
	r.liveLines = 0
}

func (r *Renderer) drawLiveLocked() {
	if !r.live || r.waiting {
		return
	}
	width := termWidth(r.w)
	maxRows := r.effectiveMaxRowsLocked()

	shown := r.plan.activeOrder
	overflow := r.plan.droppedActive
	if len(shown) > maxRows {
		overflow += len(shown) - maxRows
		shown = shown[:maxRows]
	}

	lines := 0
	for _, id := range shown {
		n := r.plan.nodes[id]
		if n == nil {
			continue
		}
		fmt.Fprintln(r.w, truncateToWidth(r.rowLineLocked(n), width))
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

func (r *Renderer) rowLineLocked(n *stageNode) string {
	glyph := r.colorFor(color.FgCyan).Sprint(spinnerFrame(n.frame))
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s", glyph, n.title)
	if r.plan.final {
		if idx, count, ok := r.plan.siblingPosition(n.id); ok && count > 1 {
			fmt.Fprintf(&b, " %s", r.colorFor(color.Faint).Sprintf("(%d/%d)", idx, count))
		}
	}
	fmt.Fprintf(&b, "  %s", r.colorFor(color.Faint).Sprint(formatDuration(r.plan.now().Sub(n.started))))
	switch {
	case n.total != nil:
		fmt.Fprintf(&b, "  %s %d/%d", bar(n.current, *n.total), n.current, *n.total)
	case n.message != "" && n.message != n.title:
		fmt.Fprintf(&b, "  %s", r.colorFor(color.Faint).Sprintf("— %s", n.message))
	}
	return b.String()
}

func (r *Renderer) okColor() *color.Color { return r.colorFor(color.FgGreen) }

func (r *Renderer) colorFor(attrs ...color.Attribute) *color.Color {
	c := color.New(attrs...)
	if r.color {
		c.EnableColor()
	} else {
		c.DisableColor()
	}
	return c
}

func (r *Renderer) emitJSON(kind string, fields map[string]any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.emitJSONLocked(kind, fields)
}

func (r *Renderer) emitJSONLocked(kind string, fields map[string]any) {
	rec := map[string]any{"type": kind}
	for k, v := range fields {
		rec[k] = v
	}
	raw, err := json.Marshal(rec)
	if err != nil {
		return
	}
	fmt.Fprintln(r.w, string(raw))
}

func fallbackTitle(phase deploymentsv1.Phase, message string) string {
	if phase == deploymentsv1.Phase_PHASE_UNSPECIFIED {
		return message
	}
	return phaseLabel(phase)
}

func phaseLabel(p deploymentsv1.Phase) string {
	switch p {
	case deploymentsv1.Phase_PHASE_UPLOADING:
		return "Uploading"
	case deploymentsv1.Phase_PHASE_PROVISIONING:
		return "Provisioning"
	case deploymentsv1.Phase_PHASE_FINALIZING:
		return "Finalizing"
	case deploymentsv1.Phase_PHASE_DELETING:
		return "Deleting"
	default:
		return "Working"
	}
}

func phaseTag(p deploymentsv1.Phase) string {
	if p == deploymentsv1.Phase_PHASE_UNSPECIFIED {
		return "progress"
	}
	return strings.ToLower(phaseLabel(p))
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
