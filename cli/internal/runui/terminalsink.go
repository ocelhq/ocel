package runui

import (
	"io"

	"github.com/ocelhq/ocel/cli/internal/events"
)

func NewTerminalSink(present Presentation, w io.Writer) events.Sink {
	switch {
	case present.Format == FormatJSON:
		return NewJSONSink(w)
	case present.Live():
		return NewLineSink(w, present)
	default:
		return NewGroupedSink(w, present)
	}
}
