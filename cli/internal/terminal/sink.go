package terminal

import (
	"io"

	"github.com/ocelhq/ocel/cli/internal/run"
)

func NewSink(present Presentation, w io.Writer) run.Sink {
	switch {
	case present.Format == FormatJSON:
		return NewJSONLines(w)
	case present.Live() && enableVirtualTerminal(w) == nil:
		return NewLiveTranscript(w, present)
	default:
		return NewTranscript(w, present)
	}
}
