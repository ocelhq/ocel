// Package deployui renders the CLI's deploy/preview/bootstrap flow. In a
// terminal it shows a compact, phased view — one spinner per phase, a progress
// bar where the provider reports counts — and streams the full detail to
// .ocel/logs/ocel.log. When stdout is not a terminal, or verbose is set, it
// bypasses the phased view and streams every event to stdout as well, so CI
// logs and `--verbose` runs keep the raw, debuggable output.
package deployui

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fatih/color"
	"github.com/mattn/go-isatty"

	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
)

const (
	okMark    = "✓"
	failMark  = "✗"
	warnMark  = "⚠"
	barWidth  = 12
	frameRate = 100 * time.Millisecond
)

// spinnerFrames is the braille animation shown against the active phase.
var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// Session renders a single deploy/preview/bootstrap run. Construct it with New,
// feed it provider events with Event, mark CLI-side building with Building, and
// end it with exactly one of Deployed, Finish, Fail, or Cancel. Close releases
// the log file.
//
// In the phased (clean) view a single background goroutine owns every animation
// write to stdout; the caller's goroutine only mutates step state under mu, so
// there is no torn write to the terminal. A Session is not meant to be shared
// across unrelated callers.
type Session struct {
	out     io.Writer
	command string // e.g. "ocel deploy", used in the cancel hint
	start   time.Time

	log     *os.File
	logPath string

	clean bool // phased UI (true) vs raw stream (false)

	mu        sync.Mutex
	active    bool // a step spinner is painting
	frame     int
	stepKey   string
	stepTitle string
	stepMsg   string
	stepCur   uint32
	stepTotal *uint32
	stepStart time.Time

	stopRender chan struct{} // closed to stop the render loop
	renderDone chan struct{} // closed when the render loop has exited
}

// New creates a Session writing its human-facing view to stdout and its full
// detail to <projectDir>/.ocel/logs/ocel.log (truncated per run). The phased
// view is used only when stdout is a real terminal and verbose is false;
// otherwise every event is streamed to stdout as well. command is the command
// to suggest on cancel (e.g. "ocel deploy"). A log that cannot be opened is not
// fatal: the run proceeds with no log file.
func New(stdout io.Writer, projectDir, command string, verbose bool) *Session {
	s := &Session{
		out:     stdout,
		command: command,
		start:   time.Now(),
		clean:   isTTY(stdout) && !verbose,
	}
	logDir := filepath.Join(projectDir, ".ocel", "logs")
	if err := os.MkdirAll(logDir, 0o755); err == nil {
		p := filepath.Join(logDir, "ocel.log")
		if f, err := os.Create(p); err == nil {
			s.log = f
			s.logPath = p
		}
	}
	if s.clean {
		s.stopRender = make(chan struct{})
		s.renderDone = make(chan struct{})
		go s.renderLoop()
	}
	return s
}

// renderLoop repaints the active step's spinner line every frame. It is the
// only writer of animation frames to stdout, so no lock is needed against the
// caller beyond mu, which it takes to read step state.
func (s *Session) renderLoop() {
	defer close(s.renderDone)
	t := time.NewTicker(frameRate)
	defer t.Stop()
	for {
		select {
		case <-s.stopRender:
			return
		case <-t.C:
			s.mu.Lock()
			if s.active {
				s.paintLocked()
				s.frame++
			}
			s.mu.Unlock()
		}
	}
}

// BuildWriter is where CLI-side build/collect subprocess output should be
// written. In the phased view it goes only to the log (so it never corrupts the
// spinner); in raw mode it is teed to stdout as well.
func (s *Session) BuildWriter() io.Writer {
	if s.clean {
		if s.log != nil {
			return s.log
		}
		return io.Discard
	}
	if s.log != nil {
		return io.MultiWriter(s.out, s.log)
	}
	return s.out
}

// Building starts the CLI-side build phase. It runs before any provider is
// spawned, so it never arrives as a wire event.
func (s *Session) Building() {
	s.logf("[building] Building project")
	if !s.clean {
		fmt.Fprintln(s.out, "Building project")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key, title := stepIdentity(deploymentsv1.Phase_PHASE_BUILDING, "")
	s.startLocked(key, title)
}

// BuildOK finalizes the CLI-side build phase after it succeeds, so a failure in
// the gap before the first provider event (spawn, preflight) is not
// misattributed to building. It is a no-op in raw mode.
func (s *Session) BuildOK() {
	s.finishStep(color.New(color.FgGreen), okMark, "")
}

// Event renders a single provider DeployEvent. The terminal ResultEvent is the
// caller's to handle (via Deployed/Fail); Event ignores it.
func (s *Session) Event(ev *deploymentsv1.DeployEvent) {
	if p := ev.GetProgress(); p != nil {
		s.progress(p.GetPhase(), p.GetMessage(), p.GetCurrent(), p.Total)
		return
	}
	if l := ev.GetLog(); l != nil {
		s.logMessage(l.GetMessage())
	}
}

func (s *Session) progress(phase deploymentsv1.Phase, message string, current uint32, total *uint32) {
	s.logf("[%s] %s", phaseTag(phase), progressLogLine(message, current, total))
	if !s.clean {
		fmt.Fprintln(s.out, message)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key, title := stepIdentity(phase, message)
	if key != s.stepKey {
		s.finalizeLocked(color.New(color.FgGreen), okMark, "")
		s.startLocked(key, title)
	}
	s.renderLocked(message, current, total)
}

func (s *Session) logMessage(message string) {
	s.logf("[log] %s", message)
	if !s.clean {
		fmt.Fprintln(s.out, message)
	}
}

// Deployed renders the success screen for deploy/preview: a green headline with
// the total duration, the featured app URLs, and a pointer to the log. Other
// connection outputs are written to the log only.
func (s *Session) Deployed(headline string, appURLs []string, outputs []*deploymentsv1.ResourceOutput) {
	s.logOutputs(outputs)
	s.finishStep(color.New(color.FgGreen), okMark, "")

	fmt.Fprintln(s.out)
	color.New(color.FgGreen, color.Bold).Fprintf(s.out, "%s %s in %s\n", okMark, headline, formatDuration(time.Since(s.start)))
	if len(appURLs) > 0 {
		fmt.Fprintln(s.out)
		url := color.New(color.FgCyan, color.Bold)
		for _, u := range appURLs {
			url.Fprintf(s.out, "  %s\n", u)
		}
	}
	s.printLogPointer("Details")
}

// Finish renders a generic success line (used by bootstrap, which has no URL):
// a green headline with the total duration and a log pointer.
func (s *Session) Finish(headline string) {
	s.finishStep(color.New(color.FgGreen), okMark, "")
	fmt.Fprintln(s.out)
	color.New(color.FgGreen, color.Bold).Fprintf(s.out, "%s %s (%s)\n", okMark, headline, formatDuration(time.Since(s.start)))
	s.printLogPointer("Details")
}

// Fail marks the active phase failed ("✗ Provisioning failed"), prints the
// error, and points at the log. In raw mode, where no phase is active, it prints
// a generic failure headline instead.
func (s *Session) Fail(err error) {
	s.logf("[error] %v", err)
	red := color.New(color.FgRed, color.Bold)
	if !s.finishStep(red, failMark, "failed") {
		red.Fprintf(s.out, "%s Failed\n", failMark)
	}
	for _, line := range strings.Split(strings.TrimRight(err.Error(), "\n"), "\n") {
		fmt.Fprintf(s.out, "  %s\n", line)
	}
	s.printLogPointer("Full log")
}

// Cancel marks the active phase cancelled ("⚠ Provisioning cancelled") and warns
// that infrastructure may be partially provisioned.
func (s *Session) Cancel() {
	s.logf("[cancelled] interrupted")
	warn := color.New(color.FgYellow, color.Bold)
	if !s.finishStep(warn, warnMark, "cancelled") {
		warn.Fprintf(s.out, "%s Cancelled\n", warnMark)
	}
	fmt.Fprintln(s.out, "  Resources may be partially created.")
	fmt.Fprintf(s.out, "  Re-run `%s` to reconcile.\n", s.command)
	s.printLogPointer("Log")
}

// Close stops the render loop and closes the log file.
func (s *Session) Close() error {
	if s.stopRender != nil {
		close(s.stopRender)
		<-s.renderDone
		s.stopRender = nil
	}
	if s.log != nil {
		return s.log.Close()
	}
	return nil
}

// finishStep finalizes the active step (if any) with the given mark and status
// word, taking the lock. It reports whether a step was actually active, so
// callers can supply a fallback headline in raw mode. A status of "" renders a
// completed step ("✓ Building  2.1s"); a status like "failed" renders a
// terminal step ("✗ Provisioning failed").
func (s *Session) finishStep(c *color.Color, mark, status string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.finalizeLocked(c, mark, status)
}

// startLocked begins a new active step. The render loop paints it from here.
func (s *Session) startLocked(key, title string) {
	s.stepKey = key
	s.stepTitle = title
	s.stepMsg = ""
	s.stepCur = 0
	s.stepTotal = nil
	s.stepStart = time.Now()
	s.frame = 0
	s.active = true
	s.paintLocked()
}

func (s *Session) renderLocked(message string, current uint32, total *uint32) {
	s.stepMsg = message
	s.stepCur = current
	s.stepTotal = total
	if s.active {
		s.paintLocked()
	}
}

// paintLocked repaints the active step's line in place (carriage return + clear
// to end of line). Must be called with the lock held.
func (s *Session) paintLocked() {
	glyph := color.New(color.FgCyan).Sprint(spinnerFrames[s.frame%len(spinnerFrames)])
	fmt.Fprintf(s.out, "\r\033[K%s %s", glyph, s.stepBody())
}

// finalizeLocked clears the active step's line and prints its final, static
// line, then clears step state so a later finalize is a no-op. A status of ""
// prints the completed form (title + dim elapsed); any other status is rendered
// in the mark's colour after the title ("✗ Provisioning failed"). Reports
// whether a step was active.
func (s *Session) finalizeLocked(c *color.Color, mark, status string) bool {
	if !s.active {
		return false
	}
	fmt.Fprint(s.out, "\r\033[K")
	if status == "" {
		c.Fprintf(s.out, "%s %s", mark, s.stepTitle)
		color.New(color.Faint).Fprintf(s.out, "  %s\n", formatDuration(time.Since(s.stepStart)))
	} else {
		c.Fprintf(s.out, "%s %s %s\n", mark, s.stepTitle, status)
	}
	s.active = false
	s.stepKey = ""
	s.stepTitle = ""
	return true
}

// stepBody builds the text after the spinner glyph: the phase title, elapsed
// time, and either a progress bar (determinate step) or the latest message
// (indeterminate step). Must be called with the lock held.
func (s *Session) stepBody() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s  %s", s.stepTitle, color.New(color.Faint).Sprint(formatDuration(time.Since(s.stepStart))))
	switch {
	case s.stepTotal != nil:
		fmt.Fprintf(&b, "  %s %d/%d", bar(s.stepCur, *s.stepTotal), s.stepCur, *s.stepTotal)
	case s.stepMsg != "" && s.stepMsg != s.stepTitle:
		fmt.Fprintf(&b, "  %s", color.New(color.Faint).Sprintf("— %s", s.stepMsg))
	}
	return b.String()
}

func (s *Session) logf(format string, args ...any) {
	if s.log == nil {
		return
	}
	fmt.Fprintf(s.log, format+"\n", args...)
}

func (s *Session) logOutputs(outputs []*deploymentsv1.ResourceOutput) {
	for _, o := range outputs {
		s.logf("[output] %s", formatOutput(o))
	}
}

func (s *Session) printLogPointer(label string) {
	if s.logPath == "" {
		return
	}
	fmt.Fprintln(s.out)
	color.New(color.Faint).Fprintf(s.out, "  %s: %s\n", label, s.relLog())
}

// relLog renders the log path relative to the working directory when it is a
// clean descendant, else the absolute path.
func (s *Session) relLog() string {
	if wd, err := os.Getwd(); err == nil {
		if rel, err := filepath.Rel(wd, s.logPath); err == nil && !strings.HasPrefix(rel, "..") {
			return rel
		}
	}
	return s.logPath
}

func isTTY(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	return isatty.IsTerminal(f.Fd())
}

// stepIdentity maps an event to the step it belongs to. A typed phase groups
// all its messages under one step titled by the phase; an unclassified event
// (bootstrap/destroy) is its own step titled by its message.
func stepIdentity(phase deploymentsv1.Phase, message string) (key, title string) {
	if phase == deploymentsv1.Phase_PHASE_UNSPECIFIED {
		return "msg:" + message, message
	}
	return "phase:" + phase.String(), phaseLabel(phase)
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

func progressLogLine(message string, current uint32, total *uint32) string {
	if total != nil {
		return fmt.Sprintf("%s (%d/%d)", message, current, *total)
	}
	return message
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

// formatOutput renders a resource's connection details for the log, mirroring
// the CLI's historical connection-outputs listing.
func formatOutput(o *deploymentsv1.ResourceOutput) string {
	if pg := o.GetPostgres(); pg != nil {
		return fmt.Sprintf("%s: postgres://%s@%s:%d/%s", o.GetLogicalName(), pg.GetUsername(), pg.GetHost(), pg.GetPort(), pg.GetDatabase())
	}
	if b := o.GetBucket(); b != nil {
		return fmt.Sprintf("%s: bucket %s at %s", o.GetLogicalName(), b.GetBucket(), b.GetAddress())
	}
	if f := o.GetFunction(); f != nil {
		return fmt.Sprintf("%s: %s", o.GetLogicalName(), f.GetUrl())
	}
	return o.GetLogicalName()
}
