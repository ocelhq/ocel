// Package deployui renders CLI command output. A Renderer is the sole
// owner of the terminal it is given: every other part of the CLI —
// commands, subprocess output, provider events — emits structured calls to
// it and never writes to the writer directly.
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

type Format string

const (
	FormatHuman Format = "human"
	FormatJSON  Format = "json"
)

// Renderer owns a writer exclusively. Construct one per command invocation
// and route every event — progress, logs, stage plans, terminal outcomes —
// through its methods. Nothing else may write to the underlying writer.
type Renderer struct {
	w      io.Writer
	format Format
	live   bool
	color  bool

	mu           sync.Mutex
	plan         *stagePlan
	legacyActive string
	liveLines    int
	waiting      bool
	start        time.Time

	tickStop chan struct{}
	tickDone chan struct{}
}

// New builds a Renderer over w. Colour and animation are decided from w
// itself — never from os.Stdout — so redirecting the writer this Renderer
// is given produces plain, uncoloured lines. format and verbose are
// independent of the TTY check: JSON never animates, and verbose forces the
// plain line-per-event view even on a real terminal.
func NewRenderer(w io.Writer, format Format, verbose bool) *Renderer {
	live := format == FormatHuman && !verbose && IsTerminal(w)
	return newRenderer(w, format, live, IsTerminal(w))
}

// newRenderer is the constructor tests use to force the live-region path
// deterministically, without needing a real terminal behind w.
func newRenderer(w io.Writer, format Format, live, colorEnabled bool) *Renderer {
	r := &Renderer{
		w:      w,
		format: format,
		live:   live,
		color:  colorEnabled,
		plan:   newStagePlan(),
		start:  time.Now(),
	}
	if r.live {
		r.tickStop = make(chan struct{})
		r.tickDone = make(chan struct{})
		go r.tickLoop()
	}
	return r
}

func (r *Renderer) Live() bool { return r.live }

// Write lets subprocess output pass through the same single-owner path as
// every other event. Safe for concurrent callers (drain goroutines).
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
			if !r.waiting && len(r.plan.activeOrder) > 0 {
				for _, id := range r.plan.activeOrder {
					if n := r.plan.nodes[id]; n != nil {
						n.frame++
					}
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
	return nil
}

// Progress renders one ProgressEvent. When stageID is non-empty it is
// tracked as its own live-region row alongside any other stage in
// progress — this is what lets a parallel deploy show one line per app.
// An empty stageID falls back to the single-active-step behaviour keyed by
// phase, for commands that have not adopted stage plans.
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
		r.progressLegacyLocked(phase, message, current, total)
	} else {
		r.progressStageLocked(id, phase, message, current, total)
	}

	r.drawLiveLocked()
}

func (r *Renderer) progressStageLocked(id string, phase deploymentsv1.Phase, message string, current uint32, total *uint32) {
	n := r.plan.nodeFor(id)
	if n.title == "" {
		n.title = legacyStageTitle(phase, message)
	}
	_, started := r.plan.progress(id, message, current, total)
	if started {
		r.plan.activeOrder = append(r.plan.activeOrder, id)
	}
	if n.state == stageDone {
		r.commitLocked(n, r.okColor(), okMark, "")
	}
}

// progressLegacyLocked reproduces the pre-stage-plan behaviour: one active
// step at a time, keyed by phase (or by message for an unspecified phase),
// finalized the moment a differently-keyed step begins.
func (r *Renderer) progressLegacyLocked(phase deploymentsv1.Phase, message string, current uint32, total *uint32) {
	key := legacyStageID(phase, message)
	if r.legacyActive != "" && r.legacyActive != key {
		if n := r.plan.nodes[r.legacyActive]; n != nil {
			r.commitLocked(n, r.okColor(), okMark, "")
		}
	}
	r.legacyActive = key
	n := r.plan.nodeFor(key)
	if n.title == "" {
		n.title = legacyStageTitle(phase, message)
	}
	_, started := r.plan.progress(key, message, current, total)
	if started {
		r.plan.activeOrder = append(r.plan.activeOrder, key)
	}
}

// StagePlan grows the stage tree. Order is declaration order, root stages
// have no parent, and a plan can arrive across several events until final.
func (r *Renderer) StagePlan(ev *deploymentsv1.StagePlanEvent) {
	if r.format == FormatJSON {
		r.emitJSON("stagePlan", map[string]any{"final": ev.GetFinal(), "count": len(ev.GetStages())})
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.plan.apply(ev)
}

// Log renders a free-form log line (Pulumi engine output, build warnings).
func (r *Renderer) Log(message string) {
	if r.format == FormatJSON {
		r.emitJSON("log", map[string]any{"message": message})
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.live {
		r.eraseLiveLocked()
		fmt.Fprintln(r.w, message)
		r.drawLiveLocked()
		return
	}
	fmt.Fprintln(r.w, message)
}

func (r *Renderer) Building() {
	r.Progress(nil, deploymentsv1.Phase_PHASE_BUILDING, "Building project", 0, nil)
}

func (r *Renderer) BuildOK() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.finishAllLocked(r.okColor(), okMark, "")
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

// Deployed, Finish, Fail and Cancel all conclude the run: they finalize
// whatever rows are still live and print the outcome. Exactly one of them
// is called once per Session.
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

// finishAllLocked commits every row still in the live region — used when
// the whole run concludes, so nothing is left spinning. It reports whether
// there was any row to finalize.
func (r *Renderer) finishAllLocked(c *color.Color, mark, status string) bool {
	if len(r.plan.activeOrder) == 0 {
		return false
	}
	r.eraseLiveLocked()
	for _, id := range r.plan.activeOrder {
		if n := r.plan.nodes[id]; n != nil {
			r.finalizeLineLocked(n, c, mark, status)
		}
	}
	r.plan.activeOrder = nil
	r.legacyActive = ""
	return true
}

// commitLocked finalizes a single row that finished mid-run (its progress
// reached its total) while leaving the rest of the live region running —
// this is what lets one app in a parallel deploy finish while others
// continue.
func (r *Renderer) commitLocked(n *stageNode, c *color.Color, mark, status string) {
	r.finalizeLineLocked(n, c, mark, status)
	r.plan.removeActive(n.id)
	if r.legacyActive == n.id {
		r.legacyActive = ""
	}
}

func (r *Renderer) finalizeLineLocked(n *stageNode, c *color.Color, mark, status string) {
	if status == "" {
		c.Fprintf(r.w, "%s %s", mark, n.title)
		r.colorFor(color.Faint).Fprintf(r.w, "  %s\n", formatDuration(time.Since(n.started)))
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
	for _, id := range r.plan.activeOrder {
		n := r.plan.nodes[id]
		if n == nil {
			continue
		}
		fmt.Fprintln(r.w, r.rowLineLocked(n))
	}
	r.liveLines = len(r.plan.activeOrder)
}

func (r *Renderer) rowLineLocked(n *stageNode) string {
	glyph := r.colorFor(color.FgCyan).Sprint(spinnerFrame(n.frame))
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s  %s", glyph, n.title, r.colorFor(color.Faint).Sprint(formatDuration(time.Since(n.started))))
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

func legacyStageID(phase deploymentsv1.Phase, message string) string {
	if phase == deploymentsv1.Phase_PHASE_UNSPECIFIED {
		return "legacy:msg:" + message
	}
	return "legacy:phase:" + phase.String()
}

func legacyStageTitle(phase deploymentsv1.Phase, message string) string {
	if phase == deploymentsv1.Phase_PHASE_UNSPECIFIED {
		return message
	}
	return phaseLabel(phase)
}

func phaseLabel(p deploymentsv1.Phase) string {
	switch p {
	case deploymentsv1.Phase_PHASE_BUILDING:
		return "Building"
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
	if d < time.Minute {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	m := int(d / time.Minute)
	sec := int((d % time.Minute) / time.Second)
	return fmt.Sprintf("%dm%02ds", m, sec)
}
