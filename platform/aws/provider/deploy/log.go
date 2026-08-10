package deploy

import (
	"bytes"
	"strings"
)

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

func (w *lineForwarder) Flush() {
	if w == nil {
		return
	}
	if line := strings.TrimRight(string(w.buf), "\r"); line != "" {
		w.log(line)
	}
	w.buf = nil
}
