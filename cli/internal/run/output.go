package run

import (
	"bytes"
	"strings"
	"sync"
)

const maxBufferedLineBytes = 64 << 10

type lineWriter struct {
	emit func(string)

	mu      sync.Mutex
	pending []byte
}

func (w *lineWriter) Write(p []byte) (int, error) {
	for _, line := range w.take(p) {
		w.emit(line)
	}
	return len(p), nil
}

func (w *lineWriter) take(p []byte) []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.pending = append(w.pending, p...)
	var ready []string
	for {
		i := bytes.IndexByte(w.pending, '\n')
		if i < 0 {
			break
		}
		line := collapseRewrites(string(w.pending[:i]))
		w.pending = w.pending[i+1:]
		if line != "" {
			ready = append(ready, line)
		}
	}
	if i := bytes.LastIndexByte(w.pending, '\r'); i >= 0 {
		w.pending = w.pending[i:]
	}
	if len(w.pending) > maxBufferedLineBytes {
		ready = append(ready, collapseRewrites(string(w.pending)))
		w.pending = nil
	}
	return ready
}

func (w *lineWriter) flush() {
	w.mu.Lock()
	line := collapseRewrites(string(w.pending))
	w.pending = nil
	w.mu.Unlock()
	if line != "" {
		w.emit(line)
	}
}

func collapseRewrites(text string) string {
	if !strings.ContainsRune(text, '\r') {
		return text
	}
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		drafts := strings.Split(line, "\r")
		lines[i] = ""
		for d := len(drafts) - 1; d >= 0; d-- {
			if drafts[d] != "" {
				lines[i] = drafts[d]
				break
			}
		}
	}
	return strings.Join(lines, "\n")
}
