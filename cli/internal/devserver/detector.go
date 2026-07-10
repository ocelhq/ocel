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

// detectInterval is how often the dev detection loop sweeps for landed
// objects. Short enough that `ocel dev` uploads complete promptly, cheap
// because each tick is one authed API call that no-ops when nothing landed.
const detectInterval = 500 * time.Millisecond

// detector runs the client-independent completion loop for `ocel dev`. Prod
// detects landings via an S3 event -> listener Lambda; dev has no event source,
// so the CLI polls the Ocel API's sweep endpoint (which HEADs the store and
// performs the atomic pending -> succeeded transition) and delivers each
// resulting op=callback to the app. The store work stays in the API - the CLI
// never touches the cloud store - and the callback originates here because the
// callback target is the app on the developer's local machine, which a managed
// API cannot reach.
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

// run sweeps on each tick until ctx is done, so the loop's lifetime is exactly
// the dev server's. Sweep errors are non-fatal - a transient API/app hiccup
// must not tear down `ocel dev` - so they're reported and the loop continues.
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

// sweep asks the API to detect and transition this project's landed objects,
// then delivers each newly-succeeded file to its app route as op=callback.
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

// postCallback delivers one completion to its app route's op=callback. The
// route (env-blind, secret-less) verifies the signature via the API and runs
// onUploadComplete. A non-2xx here means the app rejected the callback; surface
// it so the sweep reports rather than silently dropping the completion.
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
