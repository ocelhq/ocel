package telemetry

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

const flushTimeout = 3 * time.Second

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
	ctx, cancel := context.WithTimeout(ctx, flushTimeout)
	defer cancel()
	_ = Send(ctx, &http.Client{Timeout: flushTimeout}, spool, Endpoint, WriteKey)
}

func Send(ctx context.Context, client *http.Client, spool Spool, endpoint, key string) error {
	if !spool.HasEvents() {
		return nil
	}
	unlock, locked, err := spool.lockSending()
	if err != nil || !locked {
		return err
	}
	defer unlock()

	events, err := spool.Read()
	if err != nil || len(events) == 0 {
		return err
	}
	if err := postBatch(ctx, client, endpoint, key, events); err != nil {
		return err
	}
	return spool.remove(events)
}

func postBatch(ctx context.Context, client *http.Client, endpoint, key string, events []json.RawMessage) error {
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
	return nil
}
