package pulumi

import (
	"bytes"
	"strings"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

func detailWriter(progress providerkit.Progress) *lineLog {
	if progress == nil {
		return nil
	}
	return &lineLog{log: progress.Detail}
}

type lineLog struct {
	log func(string)
	buf []byte
}

func (w *lineLog) Write(p []byte) (int, error) {
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

func (w *lineLog) Flush() {
	if w == nil {
		return
	}
	if line := strings.TrimRight(string(w.buf), "\r"); line != "" {
		w.log(line)
	}
	w.buf = nil
}
