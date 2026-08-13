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

	"github.com/ocelhq/ocel/cli/internal/obs"
	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
)

const (
	okMark    = "✓"
	failMark  = "✗"
	warnMark  = "⚠"
	barWidth  = 12
	frameRate = 100 * time.Millisecond
)

var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

type Session struct {
	out     io.Writer
	command string
	start   time.Time

	log     *os.File
	logPath string

	clean bool

	mu        sync.Mutex
	active    bool
	paused    bool
	waiting   bool
	frame     int
	stepKey   string
	stepTitle string
	stepMsg   string
	stepCur   uint32
	stepTotal *uint32
	stepStart time.Time

	stopRender chan struct{}
	renderDone chan struct{}
}

func New(stdout io.Writer, projectDir, command string, verbose bool) *Session {
	return newSession(stdout, projectDir, command, isTTY(stdout) && !verbose)
}

func newSession(stdout io.Writer, projectDir, command string, clean bool) *Session {
	s := &Session{
		out:     stdout,
		command: command,
		start:   time.Now(),
		clean:   clean,
	}
	logDir := filepath.Join(projectDir, ".ocel", "logs")
	if err := os.MkdirAll(logDir, 0o755); err == nil {
		p := filepath.Join(logDir, obs.NewTraceID()+".log")
		if f, err := os.Create(p); err == nil {
			s.log = f
			s.logPath = p
		}
		_ = obs.Prune(logDir, obs.RunRetention)
	}
	if s.clean {
		s.stopRender = make(chan struct{})
		s.renderDone = make(chan struct{})
		go s.renderLoop()
	}
	return s
}

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

func (s *Session) LogPath() string {
	return s.logPath
}

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

func (s *Session) BuildOK() {
	s.finishStep(color.New(color.FgGreen), okMark, "")
}

func (s *Session) Waiting(reason, url string) {
	s.logf("[waiting] %s", withoutFragment(url))
	s.mu.Lock()
	if s.active {
		fmt.Fprint(s.out, "\r\033[K")
		s.active = false
		s.paused = true
	}
	s.waiting = true
	s.mu.Unlock()

	fmt.Fprintf(s.out, "\n%s\n  Fill them in at:\n\n    %s\n\n  Waiting for the page — press Ctrl-C to abort. Nothing has been provisioned.\n\n",
		reason, url)
}

func (s *Session) Resume() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.waiting = false
	if s.paused {
		s.paused = false
		s.active = true
		s.paintLocked()
	}
}

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

func (s *Session) Finish(headline string) {
	s.finishStep(color.New(color.FgGreen), okMark, "")
	fmt.Fprintln(s.out)
	color.New(color.FgGreen, color.Bold).Fprintf(s.out, "%s %s (%s)\n", okMark, headline, formatDuration(time.Since(s.start)))
	s.printLogPointer("Details")
}

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

func (s *Session) Cancel() {
	s.logf("[cancelled] interrupted")
	s.mu.Lock()
	waiting := s.waiting
	s.mu.Unlock()

	warn := color.New(color.FgYellow, color.Bold)
	if !s.finishStep(warn, warnMark, "cancelled") {
		warn.Fprintf(s.out, "%s Cancelled\n", warnMark)
	}
	if waiting {
		fmt.Fprintln(s.out, "  Nothing has been provisioned.")
	} else {
		fmt.Fprintln(s.out, "  Resources may be partially created.")
	}
	fmt.Fprintf(s.out, "  Re-run `%s` to reconcile.\n", s.command)
	s.printLogPointer("Log")
}

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

func (s *Session) finishStep(c *color.Color, mark, status string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.finalizeLocked(c, mark, status)
}

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

func (s *Session) paintLocked() {
	glyph := color.New(color.FgCyan).Sprint(spinnerFrames[s.frame%len(spinnerFrames)])
	fmt.Fprintf(s.out, "\r\033[K%s %s", glyph, s.stepBody())
}

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

func (s *Session) relLog() string {
	if wd, err := os.Getwd(); err == nil {
		if rel, err := filepath.Rel(wd, s.logPath); err == nil && !strings.HasPrefix(rel, "..") {
			return rel
		}
	}
	return s.logPath
}

func withoutFragment(url string) string {
	if i := strings.Index(url, "#"); i >= 0 {
		return url[:i]
	}
	return url
}

func isTTY(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	return isatty.IsTerminal(f.Fd())
}

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
