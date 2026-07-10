package deploy

import (
	"bytes"
	"strings"
)

// lineWriter adapts a per-line log callback to an io.Writer so Pulumi's
// progress streams can be forwarded as discrete log events. It returns nil
// when log is nil, so callers can skip wiring a stream at all. Callers must
// Flush the returned writer once the stream is done to emit any final line
// that wasn't newline-terminated.
func lineWriter(log func(string)) *lineForwarder {
	if log == nil {
		return nil
	}
	return &lineForwarder{log: log}
}

type lineForwarder struct {
	log func(string)
	buf []byte
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

// Flush emits any buffered remainder that never received a terminating
// newline, then clears the buffer. It is safe to call on a nil forwarder
// (when logging was disabled) and safe to call more than once.
func (w *lineForwarder) Flush() {
	if w == nil {
		return
	}
	if line := strings.TrimRight(string(w.buf), "\r"); line != "" {
		w.log(line)
	}
	w.buf = nil
}
