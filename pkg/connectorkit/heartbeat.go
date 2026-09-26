package connectorkit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
	said    spoken
}

type spoken struct {
	unreached bool
	status    int
}

func beatFor(spec Spec) (*beat, error) {
	origin, err := originOf(spec.Console)
	if err != nil {
		return nil, err
	}
	return &beat{
		origin:       origin,
		connectorID:  spec.ConnectorID,
		version:      spec.Version,
		capabilities: spec.Grants,
		identity:     spec.Identity,
		keyPath:      spec.KeyPath,
		configPath:   spec.ConfigPath,
		client:       &http.Client{Timeout: 20 * time.Second},
		every:        beatEvery,
	}, nil
}

// Heartbeat sends the console one heartbeat and answers its status, for a
// connector that is woken on a schedule rather than left running between
// requests. Serve beats on its own; this is for the hosts that cannot.
func Heartbeat(ctx context.Context, spec Spec) (int, error) {
	if !spec.Identity.HasKey() {
		return 0, errors.New("connectorkit: this connector has no identity, so it has nothing to sign a heartbeat with")
	}
	beating, err := beatFor(spec)
	if err != nil {
		return 0, err
	}
	return beating.once(ctx)
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
			fmt.Printf("connector %s: the console no longer knows this connector, and %s could not be removed: %v\n",
				b.connectorID, path, err)
		}
	}
	fmt.Printf("connector %s: the console no longer knows this connector, so its key and config are gone\n", b.connectorID)
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
			if !b.said.unreached {
				fmt.Printf("connector %s: heartbeat did not reach %s: %v\n", b.connectorID, b.origin, err)
				b.said = spoken{unreached: true}
			}
		case status >= 200 && status < 300:
			b.greeted = true
			b.said = spoken{}
		case status == http.StatusNotFound && b.greeted:
			b.wipe()
			close(retired)
			return
		default:
			if b.said.unreached || b.said.status != status {
				fmt.Printf("connector %s: heartbeat to %s answered %d\n", b.connectorID, b.origin, status)
				b.said = spoken{status: status}
			}
		}

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
