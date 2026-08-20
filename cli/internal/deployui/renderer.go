package deployui

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/fatih/color"

	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
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

func (r *Renderer) Progress(stageID []byte, phase progressv1.Phase, message string, current uint32, total *uint32) {
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

func (r *Renderer) progressStageLocked(id string, phase progressv1.Phase, message string, current uint32, total *uint32) {
	if n, ok := r.plan.nodes[id]; ok && n.state == stageDone {
		return
	}
	n, tracked := r.plan.progress(id, message, current, total)
	if n.title == "" {
		n.title = fallbackTitle(phase, message)
	}
	if !tracked {
		return
	}
	r.plan.ensureActive(id)
}

func (r *Renderer) progressUntaggedLocked(phase progressv1.Phase, message string, current uint32, total *uint32) {
	if n := r.plan.nodes[untaggedStageID]; n != nil && n.message != message && r.plan.isActive(untaggedStageID) {
		r.commitRowLocked(displayRow{n: n}, r.okColor(), okMark, "")
	}
	n, tracked := r.plan.progress(untaggedStageID, message, current, total)
	n.title = fallbackTitle(phase, message)
	if tracked {
		r.plan.ensureActive(untaggedStageID)
	}
}

func (r *Renderer) StagePlan(ev *progressv1.StagePlanEvent) {
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

func (r *Renderer) Degraded(need, detail string) {
	if r.format == FormatJSON {
		r.emitJSON("degraded", map[string]any{"need": need, "detail": detail})
		return
	}
	line := fmt.Sprintf("%s %s: %s", r.colorFor(color.FgYellow, color.Bold).Sprint(warnMark), need, detail)
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.live {
		r.eraseLiveLocked()
		fmt.Fprintln(r.w, line)
		r.drawLiveLocked()
		return
	}
	fmt.Fprintln(r.w, line)
}

func (r *Renderer) DNSOwed(headline string, records []*progressv1.DnsRecord, notes []string) {
	if len(records) == 0 {
		return
	}
	if r.format == FormatJSON {
		r.emitJSON("dnsOwed", map[string]any{
			"headline": headline,
			"records":  dnsJSON(records),
			"notes":    notes,
		})
		return
	}
	block := r.dnsBlock(headline, records, notes)
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.live {
		r.eraseLiveLocked()
		fmt.Fprint(r.w, block)
		r.drawLiveLocked()
		return
	}
	fmt.Fprint(r.w, block)
}

func (r *Renderer) dnsBlock(headline string, records []*progressv1.DnsRecord, notes []string) string {
	var b strings.Builder
	b.WriteString("\n")
	fmt.Fprintf(&b, "%s %s\n\n",
		r.colorFor(color.FgYellow, color.Bold).Sprint(warnMark),
		r.colorFor(color.Bold).Sprint(dnsHeadline(headline, records)),
	)
	head, rows := dnsRows(records, termWidth(r.w))
	if head == "" {
		for _, line := range dnsStack(records) {
			b.WriteString(line + "\n")
		}
	} else {
		b.WriteString(r.colorFor(color.Faint).Sprint(head) + "\n")
		for _, row := range rows {
			b.WriteString(row + "\n")
		}
	}
	for i, note := range dnsNotes(records, notes) {
		if i == 0 {
			b.WriteString("\n")
		}
		b.WriteString(r.colorFor(color.Faint).Sprintf("%s%s\n", dnsIndent, note))
	}
	b.WriteString("\n")
	return b.String()
}

func dnsJSON(records []*progressv1.DnsRecord) []map[string]any {
	out := make([]map[string]any, 0, len(records))
	for _, rec := range records {
		out = append(out, map[string]any{
			"name":    rec.GetName(),
			"type":    rec.GetType(),
			"value":   rec.GetValue(),
			"proxied": rec.GetProxied(),
		})
	}
	return out
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
	wasDone := n.state == stageDone
	n.state = stageDone
	n.doneFailed = failed
	n.doneDur = duration
	if !r.plan.isActive(id) {
		if !failed || wasDone {
			return
		}
		r.eraseLiveLocked()
		r.commitRowLocked(displayRow{n: n}, r.colorFor(color.FgRed, color.Bold), failMark, "failed")
		r.drawLiveLocked()
		return
	}
	if r.plan.hasActiveAncestor(id) {
		r.eraseLiveLocked()
		r.drawLiveLocked()
		return
	}
	r.eraseLiveLocked()
	for _, row := range r.plan.subtreeRows(id) {
		if row.n.id != id && row.n.state != stageDone {
			continue
		}
		r.commitRowLocked(row, r.okColor(), okMark, "")
	}
	r.drawLiveLocked()
}

func (r *Renderer) Building() {
	id := buildStageID
	if r.format == FormatJSON {
		id = nil
	}
	r.Progress(id, progressv1.Phase_PHASE_UNSPECIFIED, "Building project", 0, nil)
}

func (r *Renderer) BuildOK() {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := stageKey(buildStageID)
	n, ok := r.plan.nodes[key]
	if !ok || !r.plan.isActive(key) {
		return
	}
	r.eraseLiveLocked()
	r.commitRowLocked(displayRow{n: n}, r.okColor(), okMark, "")
	r.drawLiveLocked()
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

type Flip struct {
	Note  string
	Bound *progressv1.FlipBound
}

func (r *Renderer) Deployed(headline string, appURLs []string, note string, flip Flip, logPath string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.finishAllLocked(r.okColor(), okMark, "")

	if r.format == FormatJSON {
		fields := map[string]any{
			"headline":   headline,
			"appUrls":    appURLs,
			"durationMs": time.Since(r.start).Milliseconds(),
			"logPath":    logPath,
		}
		if note != "" {
			fields["urlNote"] = note
		}
		if flip.Bound != nil {
			fields["flipBound"] = map[string]any{
				"typicalMs": flip.Bound.GetTypicalMs(),
				"published": flip.Bound.GetPublished(),
			}
		}
		r.emitJSONLocked("deployed", fields)
		return
	}

	fmt.Fprintln(r.w)
	r.colorFor(color.FgGreen, color.Bold).Fprintf(r.w, "%s %s in %s\n", okMark, headline, formatDuration(time.Since(r.start)))
	switch {
	case len(appURLs) > 0:
		fmt.Fprintln(r.w)
		url := r.colorFor(color.FgCyan, color.Bold)
		for _, u := range appURLs {
			url.Fprintf(r.w, "  %s\n", u)
		}
	case note != "":
		fmt.Fprintln(r.w)
		r.colorFor(color.FgYellow).Fprintf(r.w, "  %s\n", note)
	}
	if flip.Note != "" {
		fmt.Fprintln(r.w)
		r.colorFor(color.Faint).Fprintf(r.w, "  %s\n", flip.Note)
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
	rows := r.plan.displayRows()
	if len(rows) == 0 {
		return false
	}
	r.eraseLiveLocked()
	for _, row := range rows {
		r.commitRowLocked(row, c, mark, status)
	}
	r.plan.activeOrder = nil
	return true
}

func (r *Renderer) commitRowLocked(row displayRow, c *color.Color, mark, status string) {
	n := row.n
	if n.state != stageDone {
		n.state = stageDone
		n.doneFailed = status == "failed"
		n.doneDur = r.plan.now().Sub(n.started)
	}
	if status == "" || status == "failed" {
		fmt.Fprintln(r.w, r.doneLineLocked(n, row.depth))
	} else {
		r.finalizeLineLocked(n, c, mark, status, n.doneDur, row.depth)
	}
	r.plan.removeActive(n.id)
}

func (r *Renderer) doneLineLocked(n *stageNode, depth int) string {
	indent := strings.Repeat("  ", depth)
	if n.doneFailed {
		return indent + r.colorFor(color.FgRed, color.Bold).Sprintf("%s %s failed", failMark, n.title)
	}
	return fmt.Sprintf("%s%s  %s",
		indent,
		r.okColor().Sprintf("%s %s", okMark, n.title),
		r.colorFor(color.Faint).Sprint(formatDuration(n.doneDur)))
}

func (r *Renderer) finalizeLineLocked(n *stageNode, c *color.Color, mark, status string, duration time.Duration, depth int) {
	indent := strings.Repeat("  ", depth)
	if status == "" {
		c.Fprintf(r.w, "%s%s %s", indent, mark, n.title)
		r.colorFor(color.Faint).Fprintf(r.w, "  %s\n", formatDuration(duration))
		return
	}
	c.Fprintf(r.w, "%s%s %s %s\n", indent, mark, n.title, status)
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
		return r.doneLineLocked(n, row.depth)
	}
	glyph := r.colorFor(color.FgCyan).Sprint(spinnerFrame(n.frame))
	var b strings.Builder
	fmt.Fprintf(&b, "%s%s %s", indent, glyph, n.title)
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

func fallbackTitle(phase progressv1.Phase, message string) string {
	if phase == progressv1.Phase_PHASE_UNSPECIFIED {
		return message
	}
	return phaseLabel(phase)
}

func phaseLabel(p progressv1.Phase) string {
	switch p {
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

func phaseTag(p progressv1.Phase) string {
	if p == progressv1.Phase_PHASE_UNSPECIFIED {
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
