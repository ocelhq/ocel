package runtrace

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
	"github.com/ocelhq/ocel/pkg/tracing"
)

const LogFileName = "ocel.log"

const logTimeLayout = "2006-01-02T15:04:05.000Z"

type Renderer interface {
	Receive(ev *streamv1.RunEvent)
	Close() error
}

func (t *Trace) LogPath() string {
	return filepath.Join(t.dir, LogFileName)
}

func (t *Trace) RenderLog(render func(w io.Writer) Renderer) error {
	file, err := os.OpenFile(t.LogPath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	t.logFile = &runLog{file: file, secrets: &t.secrets}
	t.log = render(t.logFile)
	return nil
}

func (t *Trace) closeLog() error {
	if t.log == nil {
		return nil
	}
	return errors.Join(t.log.Close(), t.logFile.Close())
}

type runLog struct {
	file    *os.File
	secrets *tracing.SecretValues

	mu      sync.Mutex
	pending []byte
}

func (l *runLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.pending = append(l.pending, p...)
	for {
		end := bytes.IndexByte(l.pending, '\n')
		if end < 0 {
			return len(p), nil
		}
		line := l.pending[:end]
		l.pending = l.pending[end+1:]
		if err := l.writeLine(line); err != nil {
			return 0, err
		}
	}
}

func (l *runLog) writeLine(line []byte) error {
	if len(line) == 0 {
		_, err := l.file.Write([]byte{'\n'})
		return err
	}
	_, err := l.file.WriteString(time.Now().UTC().Format(logTimeLayout) + " " + l.secrets.RedactText(string(line)) + "\n")
	return err
}

func (l *runLog) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	var err error
	if len(l.pending) > 0 {
		err = l.writeLine(l.pending)
		l.pending = nil
	}
	return errors.Join(err, l.file.Close())
}
