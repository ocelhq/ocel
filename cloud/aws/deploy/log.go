package deploy

import (
	"bytes"
	"io"
	"strings"
)

// lineWriter adapts a per-line log callback to an io.Writer so Pulumi's
// progress streams can be forwarded as discrete log events. It returns nil
// when log is nil, so callers can skip wiring a stream at all.
func lineWriter(log func(string)) io.Writer {
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
