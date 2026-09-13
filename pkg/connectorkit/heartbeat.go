package connectorkit

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"time"

	"github.com/lestrrat-go/jwx/v3/jwa"
	"github.com/lestrrat-go/jwx/v3/jwt"
)

const (
	beatEvery    = 60 * time.Second
	beatLifetime = 5 * time.Minute
)

func originOf(raw string) (string, error) {
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("parse console url %q: %w", raw, err)
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("console url %q names no origin", raw)
	}
	return parsed.Scheme + "://" + parsed.Host, nil
}

type beat struct {
	origin       string
	connectorID  string
	version      string
	capabilities []string
	identity     Identity
	keyPath      string
	configPath   string
	client       *http.Client
	every        time.Duration

	greeted bool
	said    int
}

func (b *beat) token() (string, error) {
	now := time.Now()
	built, err := jwt.NewBuilder().
		Issuer(b.connectorID).
		Subject(b.connectorID).
		Audience([]string{b.origin}).
		IssuedAt(now).
		Expiration(now.Add(beatLifetime)).
		Build()
	if err != nil {
		return "", err
	}
	signed, err := jwt.Sign(built, jwt.WithKey(jwa.EdDSA(), b.identity.private))
	if err != nil {
		return "", err
	}
	return string(signed), nil
}

func (b *beat) once(ctx context.Context) (int, error) {
	token, err := b.token()
	if err != nil {
		return 0, err
	}
	body, err := json.Marshal(map[string]any{
		"version":      b.version,
		"capabilities": b.capabilities,
	})
	if err != nil {
		return 0, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost,
		b.origin+"/api/connectors/"+url.PathEscape(b.connectorID)+"/heartbeat", bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+token)

	response, err := b.client.Do(request)
	if err != nil {
		return 0, err
	}
	defer response.Body.Close()
	return response.StatusCode, nil
}

func (b *beat) wipe() {
	for _, path := range []string{b.keyPath, b.configPath} {
		if path == "" {
			continue
		}
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			fmt.Printf("connector %s: the console no longer holds this connector, and %s could not be removed: %v\n",
				b.connectorID, path, err)
		}
	}
	fmt.Printf("connector %s: the console no longer holds this connector, so its key and config are gone\n", b.connectorID)
}

func (b *beat) run(ctx context.Context, retired chan<- struct{}) {
	ticker := time.NewTicker(b.every)
	defer ticker.Stop()

	for {
		status, err := b.once(ctx)
		switch {
		case err != nil:
			if ctx.Err() != nil {
				return
			}
			if b.said != -1 {
				fmt.Printf("connector %s: heartbeat did not reach %s: %v\n", b.connectorID, b.origin, err)
				b.said = -1
			}
		case status >= 200 && status < 300:
			b.greeted = true
			b.said = 0
		case status == http.StatusNotFound && b.greeted:
			b.wipe()
			close(retired)
			return
		default:
			if b.said != status {
				fmt.Printf("connector %s: heartbeat to %s answered %d\n", b.connectorID, b.origin, status)
				b.said = status
			}
		}

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
