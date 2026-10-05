package telemetry

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/gofrs/flock"
)

const FlushTimeout = 3 * time.Second

type batch struct {
	APIKey string            `json:"api_key"`
	Events []json.RawMessage `json:"batch"`
}

func Flush(ctx context.Context) {
	resolution := Resolve(WriteKey)
	if !resolution.Enabled || resolution.Debug || Endpoint == "" {
		return
	}
	spool, err := OpenSpool()
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, FlushTimeout)
	defer cancel()
	_ = spool.Send(ctx, &http.Client{Timeout: FlushTimeout}, Endpoint, WriteKey)
}

func (s Spool) Send(ctx context.Context, client *http.Client, endpoint, key string) error {
	if err := s.ensureDir(); err != nil {
		return err
	}
	lock := flock.New(filepath.Join(s.dir, flushLockName))
	locked, err := lock.TryLock()
	if err != nil {
		return fmt.Errorf("lock telemetry flush: %w", err)
	}
	if !locked {
		return nil
	}
	defer func() { _ = lock.Unlock() }()

	events, err := s.Read()
	if err != nil || len(events) == 0 {
		return err
	}
	body, err := json.Marshal(batch{APIKey: key, Events: events})
	if err != nil {
		return fmt.Errorf("encode telemetry batch: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(endpoint, "/")+"/batch/", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build telemetry request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("send telemetry batch: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("send telemetry batch: status %d", resp.StatusCode)
	}
	return s.remove(events)
}
