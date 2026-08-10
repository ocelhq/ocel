package devserver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

const detectInterval = 500 * time.Millisecond

type detector struct {
	apiURL     string
	token      string
	projectID  string
	httpClient *http.Client
	interval   time.Duration
}

func newDetector(apiURL, token, projectID string) *detector {
	return &detector{
		apiURL:     apiURL,
		token:      token,
		projectID:  projectID,
		httpClient: &http.Client{Timeout: 30 * time.Second},
		interval:   detectInterval,
	}
}

type detectRequestBody struct {
	ProjectID string `json:"projectId"`
}

type completion struct {
	CallbackBaseURL string        `json:"callbackBaseUrl"`
	SessionID       string        `json:"sessionId"`
	File            completedFile `json:"file"`
	Signature       string        `json:"signature"`
}

type detectResponseBody struct {
	Completions []completion `json:"completions"`
}

func (d *detector) run(ctx context.Context, reportErr func(error)) {
	ticker := time.NewTicker(d.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := d.sweep(ctx); err != nil && ctx.Err() == nil && reportErr != nil {
				reportErr(err)
			}
		}
	}
}

func (d *detector) sweep(ctx context.Context) error {
	completions, err := d.detect(ctx)
	if err != nil {
		return err
	}
	for _, c := range completions {
		if err := d.postCallback(ctx, c); err != nil {
			return fmt.Errorf("deliver callback for session %s: %w", c.SessionID, err)
		}
	}
	return nil
}

func (d *detector) detect(ctx context.Context) ([]completion, error) {
	body, err := json.Marshal(detectRequestBody{ProjectID: d.projectID})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint(d.apiURL, "/api/blob/detect"), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if d.token != "" {
		req.Header.Set("Authorization", "Bearer "+d.token)
	}
	resp, err := d.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("detect: unexpected status %d", resp.StatusCode)
	}
	var decoded detectResponseBody
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return nil, err
	}
	return decoded.Completions, nil
}

func (d *detector) postCallback(ctx context.Context, c completion) error {
	body, err := json.Marshal(signedCompletion{
		SessionID: c.SessionID,
		Signature: c.Signature,
		File:      c.File,
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint(c.CallbackBaseURL, "")+"?op=callback", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := d.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode >= 300 {
		return fmt.Errorf("callback: unexpected status %d", resp.StatusCode)
	}
	return nil
}
