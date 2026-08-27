package runui

import (
	"fmt"
	"io"
	"strings"
)

type Reporter interface {
	Presentation() Presentation
	Diagnostic(message string)
	Warning(message string)
	Spin(message string) *Spinner
	Suspend() func()
}

func Plain(present Presentation, w io.Writer) Reporter {
	return plainReporter{present: present, w: w}
}

type plainReporter struct {
	present Presentation
	w       io.Writer
}

func (p plainReporter) Presentation() Presentation { return p.present }

func (p plainReporter) Diagnostic(message string) { p.say(message) }

func (p plainReporter) Warning(message string) { p.say(warnMark + " " + message) }

func (p plainReporter) Spin(message string) *Spinner {
	return StartSpinner(p.present, p.w, message)
}

func (p plainReporter) Suspend() func() { return func() {} }

func (p plainReporter) say(message string) {
	if message == "" {
		return
	}
	fmt.Fprintln(p.w, strings.TrimRight(message, "\n"))
}
