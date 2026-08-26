package runui

import (
	"context"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/mattn/go-isatty"
	"golang.org/x/term"

	"github.com/ocelhq/ocel/cli/internal/cli/cmddeps"
	"github.com/ocelhq/ocel/cli/internal/deployui"
	"github.com/ocelhq/ocel/cli/internal/exitsig"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/internal/provider"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
)

type Spec struct {
	Command string
	Dry     bool
}

type Session struct {
	r    *Renderer
	unit string
}

func Run(
	ctx context.Context,
	deps cmddeps.Deps,
	cfg *projectconfig.Config,
	spec Spec,
	stdout io.Writer,
	fn func(context.Context, *provider.Runner, *Session) error,
) error {
	if _, err := cfg.RequireProvider(); err != nil {
		return err
	}

	renderer := New(stdout, Resolve(deps, stdout))
	renderer.Start()
	session := &Session{r: renderer}

	err := provider.Drive(ctx, cfg, session.BuildWriter(), session.BuildWriter(), deps.HostTrust, func(runner *provider.Runner) error {
		return fn(ctx, runner, session)
	})
	if err != nil {
		if ctx.Err() != nil {
			return &exitsig.ExitError{Code: exitsig.InterruptCode}
		}
		renderer.Emit(Envelope{Result: &Result{Headline: "Deploy failed", Error: err.Error()}})
		return &exitsig.ExitError{Code: 1}
	}
	return nil
}

func Resolve(deps cmddeps.Deps, w io.Writer) Config {
	format := Plain
	switch {
	case deps.Format() == deployui.FormatJSON:
		format = NDJSON
	case !deps.Verbose() && isTerminal(w):
		format = Live
	}
	return Config{
		Format: format,
		Color:  format != NDJSON && os.Getenv("NO_COLOR") == "",
		Width:  width(w),
		Height: height(w),
	}
}

func (s *Session) Event(ev *progressv1.OperationEvent) {
	for _, env := range Envelopes(ev) {
		s.r.Emit(env)
	}
}

func (s *Session) Building(app string) {
	s.unit = app
	s.r.Emit(Envelope{Stages: []StageDecl{
		{ID: unitID(app), Title: app},
		{ID: buildID(app), Parent: unitID(app), Title: "building"},
	}})
	s.r.Emit(Envelope{Progress: &Progress{StageID: buildID(app), Message: "Building " + app}})
}

func (s *Session) BuildOK(failed bool) {
	if s.unit == "" {
		return
	}
	s.r.Emit(Envelope{End: &StageEnd{StageID: buildID(s.unit), Failed: failed}})
	s.unit = ""
}

func (s *Session) BuildWriter() io.Writer { return buildWriter{s} }

type buildWriter struct{ s *Session }

func (w buildWriter) Write(p []byte) (int, error) {
	if w.s.unit == "" {
		return len(p), nil
	}
	for line := range strings.SplitSeq(strings.TrimRight(string(p), "\n"), "\n") {
		w.s.r.Emit(Envelope{Log: &Log{StageID: buildID(w.s.unit), Line: line}})
	}
	return len(p), nil
}

func (s *Session) Diagnostic(message string) {
	s.r.Emit(Envelope{Log: &Log{StageID: buildID(s.unit), Line: message}})
}

func (s *Session) Waiting(reason, url string) {
	s.r.Suspend()
	s.r.Note(reason, url)
}

func (s *Session) Resume() { s.r.Start() }

func (s *Session) RestartBuild() {
	if s.unit != "" {
		s.Building(s.unit)
	}
}

func (s *Session) Finish(headline string) {
	s.r.Emit(Envelope{Result: &Result{Success: true, Headline: headline}})
}

func unitID(app string) string  { return "cli-unit-" + app }
func buildID(app string) string { return "cli-build-" + app }

func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	return ok && isatty.IsTerminal(f.Fd())
}

func width(w io.Writer) int  { return dimension(w, true, "COLUMNS", 100) }
func height(w io.Writer) int { return dimension(w, false, "LINES", 40) }

func dimension(w io.Writer, wide bool, envVar string, fallback int) int {
	if f, ok := w.(*os.File); ok {
		if cols, rows, err := term.GetSize(int(f.Fd())); err == nil {
			if wide && cols > 0 {
				return cols
			}
			if !wide && rows > 0 {
				return rows
			}
		}
	}
	if n, err := strconv.Atoi(os.Getenv(envVar)); err == nil && n > 0 {
		return n
	}
	return fallback
}
