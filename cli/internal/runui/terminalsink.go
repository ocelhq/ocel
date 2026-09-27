package runui

import (
	"io"

	"github.com/ocelhq/ocel/cli/internal/events"
)

func NewTerminalSink(present Presentation, w io.Writer) events.Sink {
	if present.Format == FormatJSON {
		return NewJSONSink(w)
	}
	return NewHumanSink(w, present)
}
