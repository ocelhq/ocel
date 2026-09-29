package runui

import (
	"io"

	"github.com/ocelhq/ocel/cli/internal/run"
)

func NewTerminalSink(present Presentation, w io.Writer) run.Sink {
	switch {
	case present.Format == FormatJSON:
		return NewJSONSink(w)
	case present.Live() && enableVirtualTerminal(w) == nil:
		return NewLineSink(w, present)
	default:
		return NewGroupedSink(w, present)
	}
}
