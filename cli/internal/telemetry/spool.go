package telemetry

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/gofrs/flock"
)

const (
	spoolMaxBytes    = 1 << 20
	spoolLockTimeout = 250 * time.Millisecond
	spoolLockRetry   = 5 * time.Millisecond
	spoolFileName    = "events.jsonl"
	spoolLockName    = "events.lock"
	flushLockName    = "flush.lock"
)

type Spool struct {
	dir      string
	maxBytes int64
}

func OpenSpool() (Spool, error) {
	base, err := os.UserCacheDir()
	if err != nil {
		return Spool{}, fmt.Errorf("resolve user cache directory: %w", err)
	}
	return NewSpool(filepath.Join(base, "ocel", "telemetry"), spoolMaxBytes), nil
}

func NewSpool(dir string, maxBytes int64) Spool {
	return Spool{dir: dir, maxBytes: maxBytes}
}

func (s Spool) Append(event Event) error {
	line, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("encode telemetry event: %w", err)
	}
	line = append(line, '\n')
	if int64(len(line)) > s.maxBytes {
		return fmt.Errorf("telemetry event is %d bytes, over the %d byte spool", len(line), s.maxBytes)
	}
	unlock, err := s.lockEvents()
	if err != nil {
		return err
	}
	defer unlock()

	if info, err := os.Stat(s.eventsPath()); err != nil || info.Size()+int64(len(line)) <= s.maxBytes {
		return s.appendLine(line)
	}
	kept, err := s.readLines()
	if err != nil {
		return err
	}
	var size int64
	for _, existing := range kept {
		size += int64(len(existing)) + 1
	}
	for len(kept) > 0 && size+int64(len(line)) > s.maxBytes {
		size -= int64(len(kept[0])) + 1
		kept = kept[1:]
	}
	return s.replaceLines(append(kept, line[:len(line)-1]))
}

func (s Spool) Read() ([]json.RawMessage, error) {
	lines, err := s.readLines()
	if err != nil {
		return nil, err
	}
	events := make([]json.RawMessage, len(lines))
	for i, line := range lines {
		events[i] = line
	}
	return events, nil
}

func (s Spool) HasEvents() bool {
	info, err := os.Stat(s.eventsPath())
	return err == nil && info.Size() > 0
}

func (s Spool) remove(sent []json.RawMessage) error {
	unlock, err := s.lockEvents()
	if err != nil {
		return err
	}
	defer unlock()
	gone := make(map[string]bool, len(sent))
	for _, event := range sent {
		gone[string(event)] = true
	}
	lines, err := s.readLines()
	if err != nil {
		return err
	}
	var kept [][]byte
	for _, line := range lines {
		if !gone[string(line)] {
			kept = append(kept, line)
		}
	}
	return s.replaceLines(kept)
}

func (s Spool) lockSending() (unlock func(), locked bool, err error) {
	lock := flock.New(filepath.Join(s.dir, flushLockName))
	locked, err = lock.TryLock()
	if err != nil {
		return nil, false, fmt.Errorf("lock telemetry flush: %w", err)
	}
	if !locked {
		return nil, false, nil
	}
	return func() { _ = lock.Unlock() }, true, nil
}

func (s Spool) eventsPath() string { return filepath.Join(s.dir, spoolFileName) }

func (s Spool) ensureDir() error {
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return fmt.Errorf("create telemetry spool directory: %w", err)
	}
	return nil
}

func (s Spool) lockEvents() (func(), error) {
	if err := s.ensureDir(); err != nil {
		return nil, err
	}
	lock := flock.New(filepath.Join(s.dir, spoolLockName))
	ctx, cancel := context.WithTimeout(context.Background(), spoolLockTimeout)
	defer cancel()
	locked, err := lock.TryLockContext(ctx, spoolLockRetry)
	if err != nil {
		return nil, fmt.Errorf("lock telemetry spool: %w", err)
	}
	if !locked {
		return nil, errors.New("lock telemetry spool: busy")
	}
	return func() { _ = lock.Unlock() }, nil
}

func (s Spool) readLines() ([][]byte, error) {
	raw, err := os.ReadFile(s.eventsPath())
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read telemetry spool: %w", err)
	}
	var lines [][]byte
	for _, line := range bytes.Split(raw, []byte{'\n'}) {
		if json.Valid(line) {
			lines = append(lines, line)
		}
	}
	return lines, nil
}

func (s Spool) appendLine(line []byte) error {
	file, err := os.OpenFile(s.eventsPath(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open telemetry spool: %w", err)
	}
	if _, err := file.Write(line); err != nil {
		file.Close()
		return fmt.Errorf("write telemetry spool: %w", err)
	}
	return file.Close()
}

func (s Spool) replaceLines(lines [][]byte) error {
	staged, err := os.CreateTemp(s.dir, ".events.*")
	if err != nil {
		return fmt.Errorf("create staged telemetry spool: %w", err)
	}
	defer os.Remove(staged.Name())
	for _, line := range lines {
		if _, err := staged.Write(append(line[:len(line):len(line)], '\n')); err != nil {
			staged.Close()
			return fmt.Errorf("write telemetry spool: %w", err)
		}
	}
	if err := staged.Close(); err != nil {
		return fmt.Errorf("write telemetry spool: %w", err)
	}
	if err := os.Rename(staged.Name(), s.eventsPath()); err != nil {
		return fmt.Errorf("replace telemetry spool: %w", err)
	}
	return nil
}
