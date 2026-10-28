package terminal

import (
	"io"
	"time"

	"github.com/ocelhq/ocel/cli/internal/run"
)

func NewRunLog(w io.Writer) run.Sink {
	ticker := time.NewTicker(time.Second)
	s := newRunLog(w, ticker.C)
	s.stopTicks = ticker.Stop
	return s
}

func newRunLog(w io.Writer, ticks <-chan time.Time) *Transcript {
	s := newTranscript(w, Presentation{Format: FormatHuman, Verbose: true, Width: defaultColumns}, ticks)
	s.streams = true
	return s
}
