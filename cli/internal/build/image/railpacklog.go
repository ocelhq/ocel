package image

import (
	"io"
	"sync"

	"github.com/charmbracelet/log"
)

func railpackLogsTo(progress io.Writer) func() {
	if progress == nil {
		progress = io.Discard
	}
	previous := log.Default()
	log.SetDefault(log.NewWithOptions(progress, log.Options{Level: log.InfoLevel}))
	return func() { log.SetDefault(previous) }
}

type lockedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}
