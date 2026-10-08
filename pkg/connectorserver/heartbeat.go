package connectorserver

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"time"

	"connectrpc.com/connect"
	"github.com/lestrrat-go/jwx/v3/jwa"
	"github.com/lestrrat-go/jwx/v3/jwt"

	consolev1 "github.com/ocelhq/ocel/pkg/proto/console/v1"
	"github.com/ocelhq/ocel/pkg/proto/console/v1/consolev1connect"
)

const (
	beatEvery    = 60 * time.Second
	beatLifetime = 5 * time.Minute

	connectRoute = "/api/connect"
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
	out          io.Writer

	greeted bool
	said    spoken
}

type spoken struct {
	unreached bool
	code      connect.Code
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
		out:          os.Stdout,
	}, nil
}

func Heartbeat(ctx context.Context, spec Spec) error {
	if !spec.Identity.HasKey() {
		return errors.New("connectorserver: this connector has no identity, so it has nothing to sign a heartbeat with")
	}
	beating, err := beatFor(spec)
	if err != nil {
		return err
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

func (b *beat) once(ctx context.Context) error {
	token, err := b.token()
	if err != nil {
		return err
	}
	consoleClient := consolev1connect.NewConnectorServiceClient(b.client, b.origin+connectRoute,
		connect.WithInterceptors(connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
			return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
				req.Header().Set("Authorization", "Bearer "+token)
				return next(ctx, req)
			}
		})))
	_, err = consoleClient.Heartbeat(ctx, &consolev1.HeartbeatConnectorRequest{
		Id:           b.connectorID,
		Version:      b.version,
		Capabilities: b.capabilities,
	})
	return err
}

func IsRefusal(err error) bool {
	var refusal *connect.Error
	if !errors.As(err, &refusal) {
		return false
	}
	switch refusal.Code() {
	case connect.CodeUnavailable, connect.CodeDeadlineExceeded, connect.CodeCanceled:
		return false
	}
	return true
}

func (b *beat) wipe() {
	for _, path := range []string{b.keyPath, b.configPath} {
		if path == "" {
			continue
		}
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			fmt.Fprintf(b.out, "connector %s: the console no longer knows this connector, and %s could not be removed: %v\n",
				b.connectorID, path, err)
		}
	}
	fmt.Fprintf(b.out, "connector %s: the console no longer knows this connector, so its key and config are gone\n", b.connectorID)
}

func (b *beat) run(ctx context.Context, retired chan<- struct{}) {
	ticker := time.NewTicker(b.every)
	defer ticker.Stop()

	for {
		err := b.once(ctx)
		switch {
		case err == nil:
			b.greeted = true
			b.said = spoken{}
		case ctx.Err() != nil:
			return
		case !IsRefusal(err):
			if !b.said.unreached {
				fmt.Fprintf(b.out, "connector %s: heartbeat did not reach %s: %v\n", b.connectorID, b.origin, err)
				b.said = spoken{unreached: true}
			}
		case connect.CodeOf(err) == connect.CodeNotFound && b.greeted:
			b.wipe()
			close(retired)
			return
		default:
			if b.said.unreached || b.said.code != connect.CodeOf(err) {
				fmt.Fprintf(b.out, "connector %s: heartbeat to %s was refused: %v\n", b.connectorID, b.origin, err)
				b.said = spoken{code: connect.CodeOf(err)}
			}
		}

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
