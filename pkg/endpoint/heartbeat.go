package endpoint

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"connectrpc.com/connect"
	"github.com/lestrrat-go/jwx/v3/jwa"
	"github.com/lestrrat-go/jwx/v3/jwt"

	scopev1 "github.com/ocelhq/ocel/pkg/proto/common/scope/v1"
	consolev1 "github.com/ocelhq/ocel/pkg/proto/console/v1"
	"github.com/ocelhq/ocel/pkg/proto/console/v1/consolev1connect"
	"github.com/ocelhq/ocel/pkg/tracing"
)

const (
	beatEvery    = 60 * time.Second
	beatLifetime = 5 * time.Minute

	connectRoute = "/api/connect"

	HeartbeatPath = "/v1/heartbeat"
)

type beat struct {
	origin     string
	endpointID string
	version    string
	scopes     []scopev1.Scope
	services   []string
	identity   Identity
	keyPath    string
	configPath string
	client     *http.Client
	every      time.Duration
	out        io.Writer

	accepted bool
	logged   loggedState
}

type loggedState struct {
	unreached bool
	code      connect.Code
}

func beatFor(spec Spec) (*beat, error) {
	origin, err := originOf(spec.Console)
	if err != nil {
		return nil, err
	}
	allowed, err := spec.ListAllowedScopes()
	if err != nil {
		return nil, err
	}
	return &beat{
		origin:     origin,
		endpointID: spec.EndpointID,
		version:    spec.Version,
		scopes:     allowed,
		services:   spec.Services(),
		identity:   spec.Identity,
		keyPath:    spec.KeyPath,
		configPath: spec.ConfigPath,
		client:     tracing.NewClient(20 * time.Second),
		every:      beatEvery,
		out:        os.Stdout,
	}, nil
}

func Heartbeat(ctx context.Context, spec Spec) error {
	if !spec.Identity.HasKey() {
		return errors.New("endpoint: this endpoint has no identity, so it has nothing to sign a heartbeat with")
	}
	beating, err := beatFor(spec)
	if err != nil {
		return err
	}
	return beating.send(ctx)
}

func serveHeartbeat(spec Spec) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := spec.VerifyHeartbeat(r); err != nil {
			fmt.Printf("endpoint %s: a heartbeat request was refused: %v\n", spec.EndpointID, err)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if err := Heartbeat(r.Context(), spec); err != nil {
			fmt.Printf("endpoint %s: heartbeat did not go through: %v\n", spec.EndpointID, err)
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func (b *beat) token() (string, error) {
	now := time.Now()
	built, err := jwt.NewBuilder().
		Issuer(b.endpointID).
		Subject(b.endpointID).
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

func (b *beat) send(ctx context.Context) error {
	token, err := b.token()
	if err != nil {
		return err
	}
	consoleClient := consolev1connect.NewEndpointServiceClient(b.client, b.origin+connectRoute,
		connect.WithInterceptors(connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
			return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
				req.Header().Set("Authorization", "Bearer "+token)
				req.Header().Set("User-Agent", "ocel-endpoint/"+b.version)
				return next(ctx, req)
			}
		})))
	_, err = consoleClient.Heartbeat(ctx, &consolev1.HeartbeatEndpointRequest{
		Id:            b.endpointID,
		Version:       b.version,
		Services:      b.services,
		AllowedScopes: b.scopes,
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

func (b *beat) removeIdentity() {
	for _, path := range []string{b.keyPath, b.configPath} {
		if path == "" {
			continue
		}
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			fmt.Fprintf(b.out, "endpoint %s: the console no longer knows this endpoint, and %s could not be removed: %v\n",
				b.endpointID, path, err)
		}
	}
	fmt.Fprintf(b.out, "endpoint %s: the console no longer knows this endpoint, so its key and config are gone\n", b.endpointID)
}

func (b *beat) run(ctx context.Context, unregistered chan<- struct{}) {
	ticker := time.NewTicker(b.every)
	defer ticker.Stop()

	for {
		err := b.send(ctx)
		switch {
		case err == nil:
			b.accepted = true
			b.logged = loggedState{}
		case ctx.Err() != nil:
			return
		case !IsRefusal(err):
			if !b.logged.unreached {
				fmt.Fprintf(b.out, "endpoint %s: heartbeat did not reach %s: %v\n", b.endpointID, b.origin, err)
				b.logged = loggedState{unreached: true}
			}
		case connect.CodeOf(err) == connect.CodeNotFound && b.accepted:
			b.removeIdentity()
			close(unregistered)
			return
		default:
			if b.logged.unreached || b.logged.code != connect.CodeOf(err) {
				fmt.Fprintf(b.out, "endpoint %s: heartbeat to %s was refused: %v\n", b.endpointID, b.origin, err)
				b.logged = loggedState{code: connect.CodeOf(err)}
			}
		}

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
