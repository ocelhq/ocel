package envsource

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	infisicalAttempts       = 5
	infisicalBackoff        = 250 * time.Millisecond
	infisicalBackoffCeiling = 8 * time.Second
	infisicalWaitCeiling    = 60 * time.Second
	infisicalTokenMargin    = 60 * time.Second
	infisicalBodyLimitBytes = 8 << 20
	infisicalRequestTimeout = 30 * time.Second
)

type infisicalClient struct {
	host       string
	http       *http.Client
	credential Credential

	mu      sync.Mutex
	session session
}

type infisicalError struct {
	Status     int
	Kind       string
	Message    string
	RetryAfter time.Duration
}

func (e *infisicalError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("Infisical answered %d", e.Status)
	}
	return fmt.Sprintf("Infisical answered %d: %s", e.Status, e.Message)
}

func isInfisicalStatus(err error, status int) bool {
	var refused *infisicalError
	return errors.As(err, &refused) && refused.Status == status
}

func retryAfterOf(err error) time.Duration {
	var refused *infisicalError
	if errors.As(err, &refused) {
		return refused.RetryAfter
	}
	return 0
}

func isInfisicalExists(err error) bool {
	var refused *infisicalError
	return errors.As(err, &refused) && strings.Contains(refused.Message, "already exists")
}

func (c *infisicalClient) logIn(ctx context.Context, method string, body map[string]string) (session, error) {
	var granted struct {
		AccessToken    string `json:"accessToken"`
		ExpiresSeconds int64  `json:"expiresIn"`
	}
	if err := c.send(ctx, http.MethodPost, "/api/v1/auth/"+method+"/login", "", body, &granted, true); err != nil {
		return session{}, fmt.Errorf("log in to Infisical with %s: %w", method, err)
	}
	if granted.AccessToken == "" {
		return session{}, fmt.Errorf("log in to Infisical with %s: the answer has no access token", method)
	}
	return session{token: granted.AccessToken, expiresAt: time.Now().Add(time.Duration(granted.ExpiresSeconds) * time.Second)}, nil
}

func (c *infisicalClient) currentSession(ctx context.Context, renew bool) (session, error) {
	c.mu.Lock()
	current := c.session
	c.mu.Unlock()
	if !renew && current.token != "" && (current.fixed || time.Now().Add(infisicalTokenMargin).Before(current.expiresAt)) {
		return current, nil
	}
	if c.credential == nil {
		return session{}, errors.New("no Infisical credential is configured")
	}
	next, err := c.credential.logIn(ctx, c)
	if err != nil {
		return session{}, err
	}
	c.mu.Lock()
	c.session = next
	c.mu.Unlock()
	return next, nil
}

func (c *infisicalClient) call(ctx context.Context, method, target string, body, out any, idempotent bool) error {
	current, err := c.currentSession(ctx, false)
	if err != nil {
		return err
	}
	err = c.send(ctx, method, target, current.token, body, out, idempotent)
	if isInfisicalStatus(err, http.StatusUnauthorized) && !current.fixed {
		if current, err = c.currentSession(ctx, true); err != nil {
			return err
		}
		return c.send(ctx, method, target, current.token, body, out, idempotent)
	}
	return err
}

func (c *infisicalClient) send(ctx context.Context, method, target, token string, body, out any, idempotent bool) error {
	var payload []byte
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return err
		}
		payload = encoded
	}
	for attempt := 1; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, method, c.host+target, bytes.NewReader(payload))
		if err != nil {
			return err
		}
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := c.http.Do(req)
		if err != nil {
			if !idempotent || attempt >= infisicalAttempts || ctx.Err() != nil {
				return fmt.Errorf("reach Infisical at %s: %w", c.host, err)
			}
			if err := pause(ctx, backoff(attempt)); err != nil {
				return err
			}
			continue
		}
		raw, readErr := io.ReadAll(io.LimitReader(resp.Body, infisicalBodyLimitBytes))
		_ = resp.Body.Close()
		if readErr != nil {
			return fmt.Errorf("read Infisical's answer: %w", readErr)
		}
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			if out == nil || len(raw) == 0 {
				return nil
			}
			if err := json.Unmarshal(raw, out); err != nil {
				return fmt.Errorf("read Infisical's answer: %w", err)
			}
			return nil
		}
		refused := infisicalErrorOf(resp.StatusCode, raw)
		retryable := resp.StatusCode == http.StatusTooManyRequests ||
			(idempotent && (resp.StatusCode == http.StatusBadGateway || resp.StatusCode == http.StatusServiceUnavailable || resp.StatusCode == http.StatusGatewayTimeout))
		wait := backoff(attempt)
		if after, ok := retryAfter(resp.Header.Get("Retry-After")); ok {
			refused.RetryAfter = after
			wait = after
		}
		if !retryable || attempt >= infisicalAttempts || !waitFits(ctx, wait) {
			return refused
		}
		if err := pause(ctx, wait); err != nil {
			return err
		}
	}
}

func infisicalErrorOf(status int, raw []byte) *infisicalError {
	var said struct {
		Error   string          `json:"error"`
		Message json.RawMessage `json:"message"`
	}
	out := &infisicalError{Status: status}
	if json.Unmarshal(raw, &said) != nil {
		return out
	}
	out.Kind = said.Error
	var text string
	if json.Unmarshal(said.Message, &text) == nil {
		out.Message = text
	} else if len(said.Message) > 0 {
		out.Message = string(said.Message)
	}
	return out
}

func retryAfter(header string) (time.Duration, bool) {
	if header == "" {
		return 0, false
	}
	seconds, err := strconv.Atoi(strings.TrimSpace(header))
	if err != nil || seconds < 0 {
		return 0, false
	}
	return time.Duration(seconds) * time.Second, true
}

func waitFits(ctx context.Context, wait time.Duration) bool {
	if wait > infisicalWaitCeiling {
		return false
	}
	deadline, bounded := ctx.Deadline()
	return !bounded || time.Until(deadline) > wait
}

func backoff(attempt int) time.Duration {
	ceiling := min(infisicalBackoff<<(attempt-1), infisicalBackoffCeiling)
	return ceiling/2 + rand.N(ceiling/2+1)
}

func pause(ctx context.Context, wait time.Duration) error {
	if wait <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
