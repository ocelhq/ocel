package pulumi

import (
	"bytes"
	"strings"
)

// lineForwarder turns the engine's byte stream into whole lines, because a
// Reporter's Detail is a line and the CLI writes in arbitrary chunks. It exists
// so no vendor has to re-derive that the two are not the same thing.
type lineForwarder struct {
	log func(string)
	buf []byte
}

func lineWriter(log func(string)) *lineForwarder {
	if log == nil {
		return nil
	}
	return &lineForwarder{log: log}
}

func (w *lineForwarder) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	for {
		i := bytes.IndexByte(w.buf, '\n')
		if i < 0 {
			break
		}
		if line := strings.TrimRight(string(w.buf[:i]), "\r"); line != "" {
			w.log(line)
		}
		w.buf = w.buf[i+1:]
	}
	return len(p), nil
}

// Flush emits whatever the engine left without a trailing newline. The last
// line of a failed run is usually the interesting one, so it must not be lost.
func (w *lineForwarder) Flush() {
	if w == nil {
		return
	}
	if line := strings.TrimRight(string(w.buf), "\r"); line != "" {
		w.log(line)
	}
	w.buf = nil
}
