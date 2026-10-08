package connectorserver

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/lestrrat-go/jwx/v3/jwa"
	"github.com/lestrrat-go/jwx/v3/jwt"
	"google.golang.org/protobuf/proto"

	consolev1 "github.com/ocelhq/ocel/pkg/proto/console/v1"
	"github.com/ocelhq/ocel/pkg/proto/console/v1/consolev1connect"
)

const connectorID = "0199b5c4-0000-7000-8000-00000000cafe"

func beating(t *testing.T, origin string) (heartbeat *beat, keyPath, configPath string) {
	heartbeat, keyPath, configPath, _ = beatingInto(t, origin)
	return heartbeat, keyPath, configPath
}

func beatingInto(t *testing.T, origin string) (heartbeat *beat, keyPath, configPath string, said *bytes.Buffer) {
	t.Helper()
	root := t.TempDir()
	keyPath = filepath.Join(root, "key")
	configPath = filepath.Join(root, "config.json")
	if err := os.WriteFile(configPath, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	identity, err := LoadOrCreateIdentity(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	said = &bytes.Buffer{}
	return &beat{
		connectorID:  connectorID,
		version:      "0.0.0-alpha",
		capabilities: []string{CapabilityVariablesRead},
		identity:     identity,
		keyPath:      keyPath,
		configPath:   configPath,
		client:       http.DefaultClient,
		every:        time.Millisecond,
		origin:       origin,
		out:          said,
	}, keyPath, configPath, said
}

type heartbeats struct {
	consolev1connect.UnimplementedConnectorServiceHandler
	answer func(seen int) error

	count atomic.Int64

	mu        sync.Mutex
	requests  []*consolev1.HeartbeatConnectorRequest
	headers   []http.Header
	procedure string
}

func (h *heartbeats) Heartbeat(_ context.Context, req *consolev1.HeartbeatConnectorRequest) (*consolev1.HeartbeatConnectorResponse, error) {
	seen := int(h.count.Add(1))
	h.mu.Lock()
	h.requests = append(h.requests, req)
	h.mu.Unlock()
	if err := h.answer(seen); err != nil {
		return nil, err
	}
	return &consolev1.HeartbeatConnectorResponse{}, nil
}

func console(t *testing.T, answer func(seen int) error) (*httptest.Server, *heartbeats) {
	return consoleBehind(t, answer, func(h http.Handler) http.Handler { return h })
}

func consoleBehind(t *testing.T, answer func(seen int) error, front func(http.Handler) http.Handler) (*httptest.Server, *heartbeats) {
	t.Helper()
	service := &heartbeats{answer: answer}
	mux := http.NewServeMux()
	path, handler := consolev1connect.NewConnectorServiceHandler(service)
	mux.Handle(connectRoute+path, http.StripPrefix(connectRoute, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		service.mu.Lock()
		service.headers = append(service.headers, r.Header.Clone())
		service.procedure = r.URL.Path
		service.mu.Unlock()
		handler.ServeHTTP(w, r)
	})))
	server := httptest.NewServer(front(mux))
	t.Cleanup(server.Close)
	return server, service
}

func droppingFirst(n int64) func(http.Handler) http.Handler {
	var dropped atomic.Int64
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if dropped.Add(1) <= n {
				conn, _, err := http.NewResponseController(w).Hijack()
				if err == nil {
					_ = conn.Close()
				}
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func accepted(int) error { return nil }

func refused(code connect.Code) func(int) error {
	return func(int) error { return connect.NewError(code, errors.New("refused")) }
}

func TestHeartbeatCallsTheConsoleWithATokenTheConnectorSigned(t *testing.T) {
	server, service := console(t, accepted)

	heartbeat, _, _ := beating(t, server.URL)
	if err := heartbeat.once(context.Background()); err != nil {
		t.Fatalf("once: %v", err)
	}
	if service.procedure != consolev1connect.ConnectorServiceHeartbeatProcedure {
		t.Fatalf("procedure = %q, want %q", service.procedure, consolev1connect.ConnectorServiceHeartbeatProcedure)
	}
	want := &consolev1.HeartbeatConnectorRequest{Id: connectorID, Version: "0.0.0-alpha", Capabilities: []string{CapabilityVariablesRead}}
	if len(service.requests) != 1 || !proto.Equal(service.requests[0], want) {
		t.Fatalf("the console read %v, want %v", service.requests, want)
	}

	raw, bearing := strings.CutPrefix(service.headers[0].Get("Authorization"), "Bearer ")
	if !bearing {
		t.Fatalf("authorization = %q", service.headers[0].Get("Authorization"))
	}
	public, err := base64.StdEncoding.DecodeString(heartbeat.identity.PublicKey())
	if err != nil {
		t.Fatal(err)
	}
	token, err := jwt.ParseString(raw,
		jwt.WithKey(jwa.EdDSA(), ed25519.PublicKey(public)),
		jwt.WithIssuer(connectorID),
		jwt.WithSubject(connectorID),
		jwt.WithAudience(server.URL),
	)
	if err != nil {
		t.Fatalf("the heartbeat token does not verify as the console verifies it: %v", err)
	}
	issued, _ := token.IssuedAt()
	expires, _ := token.Expiration()
	if lifetime := expires.Sub(issued); lifetime != beatLifetime {
		t.Errorf("the token lives %s, but the console accepts tokens at most %s old", lifetime, beatLifetime)
	}
}

func TestHeartbeatNamesTheConnectorAndItsVersionAsItsUserAgent(t *testing.T) {
	server, service := console(t, accepted)

	heartbeat, _, _ := beating(t, server.URL)
	if err := heartbeat.once(context.Background()); err != nil {
		t.Fatalf("once: %v", err)
	}
	if got := service.headers[0].Get("User-Agent"); got != "ocel-connector/0.0.0-alpha" {
		t.Errorf("user agent = %q, want ocel-connector/0.0.0-alpha", got)
	}
}

func TestHeartbeatReturnsTheRefusalTheConsoleGave(t *testing.T) {
	server, _ := console(t, refused(connect.CodeNotFound))

	heartbeat, _, _ := beating(t, server.URL)
	err := heartbeat.once(context.Background())
	if connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("once = %v, want the console's not-found refusal", err)
	}
}

func TestOneHeartbeatIsSentForAHostThatIsWokenOnASchedule(t *testing.T) {
	server, service := console(t, accepted)

	identity, err := IdentityFromSeed(make([]byte, ed25519.SeedSize))
	if err != nil {
		t.Fatal(err)
	}
	spec := Spec{
		Config:   Config{Console: server.URL, ConnectorID: connectorID, OrganizationID: "org-1", Grants: []string{CapabilityVariablesRead}},
		Version:  "0.0.0-alpha",
		Identity: identity,
	}
	if err := Heartbeat(context.Background(), spec); err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}
	if service.count.Load() != 1 || service.requests[0].GetVersion() != "0.0.0-alpha" {
		t.Fatalf("the console read %v after %d beats, want the version and capabilities of one beat", service.requests, service.count.Load())
	}
	if err := Heartbeat(context.Background(), Spec{Config: spec.Config}); err == nil {
		t.Fatal("a heartbeat with no identity was sent, and the console would refuse an unsigned one")
	}
}

func TestHeartbeatWipesAfterANotFoundThatFollowsASuccess(t *testing.T) {
	server, service := console(t, func(seen int) error {
		if seen == 1 {
			return nil
		}
		return connect.NewError(connect.CodeNotFound, errors.New("no such connector"))
	})

	heartbeat, keyPath, configPath := beating(t, server.URL)
	retired := make(chan struct{})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	heartbeat.run(ctx, retired)

	select {
	case <-retired:
	default:
		t.Fatal("the connector did not retire")
	}
	if _, err := os.Stat(keyPath); !os.IsNotExist(err) {
		t.Fatalf("the key is still there: %v", err)
	}
	if _, err := os.Stat(configPath); !os.IsNotExist(err) {
		t.Fatalf("the config is still there: %v", err)
	}
	if service.count.Load() != 2 {
		t.Fatalf("the console saw %d beats, want 2", service.count.Load())
	}
}

func TestHeartbeatKeepsTheKeyWhenTheNotFoundComesFirst(t *testing.T) {
	server, service := console(t, refused(connect.CodeNotFound))

	heartbeat, keyPath, configPath := beating(t, server.URL)
	retired := make(chan struct{})
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	heartbeat.run(ctx, retired)

	select {
	case <-retired:
		t.Fatal("a not-found before any success retired the connector")
	default:
	}
	if _, err := os.Stat(keyPath); err != nil {
		t.Fatalf("the key was wiped: %v", err)
	}
	if _, err := os.Stat(configPath); err != nil {
		t.Fatalf("the config was wiped: %v", err)
	}
	if service.count.Load() < 2 {
		t.Fatalf("the connector stopped beating after %d", service.count.Load())
	}
}

func TestHeartbeatKeepsGoingThroughOtherRefusals(t *testing.T) {
	for name, code := range map[string]connect.Code{
		"the console fails":                 connect.CodeInternal,
		"the console refuses the token":     connect.CodeUnauthenticated,
		"the console does not know the rpc": connect.CodeUnimplemented,
	} {
		t.Run(name, func(t *testing.T) {
			server, service := console(t, refused(code))

			heartbeat, keyPath, _ := beating(t, server.URL)
			retired := make(chan struct{})
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
			defer cancel()
			heartbeat.run(ctx, retired)

			if service.count.Load() < 2 {
				t.Fatalf("the connector stopped beating after %d", service.count.Load())
			}
			if _, err := os.Stat(keyPath); err != nil {
				t.Fatalf("the key was wiped: %v", err)
			}
		})
	}
}

func TestHeartbeatKeepsGoingUntilAnUnreachableConsoleAnswers(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	server, service := consoleBehind(t, func(seen int) error {
		if seen > 1 {
			cancel()
		}
		return nil
	}, droppingFirst(3))

	heartbeat, keyPath, _, said := beatingInto(t, server.URL)
	retired := make(chan struct{})
	heartbeat.run(ctx, retired)

	select {
	case <-retired:
		t.Fatal("an unreachable console retired the connector")
	default:
	}
	if _, err := os.Stat(keyPath); err != nil {
		t.Fatalf("the key was wiped: %v", err)
	}
	if service.count.Load() < 2 {
		t.Fatalf("the console answered %d beats after it came up, want the connector to keep beating until it did", service.count.Load())
	}
	if got := strings.Count(said.String(), "did not reach"); got != 1 {
		t.Errorf("the connector said %q, want one line that the heartbeat did not reach the console", said.String())
	}
	if strings.Contains(said.String(), "refused") {
		t.Errorf("the connector said %q, but a console it never reached refused nothing", said.String())
	}
}

func TestHeartbeatSaysOnceThatTheConsoleRefusedIt(t *testing.T) {
	server, _ := console(t, refused(connect.CodeUnauthenticated))

	heartbeat, _, _, said := beatingInto(t, server.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	heartbeat.run(ctx, make(chan struct{}))

	if got := strings.Count(said.String(), "was refused"); got != 1 {
		t.Errorf("the connector said %q, want one line that the console refused the heartbeat", said.String())
	}
	if strings.Contains(said.String(), "did not reach") {
		t.Errorf("the connector said %q, but the console answered", said.String())
	}
}

func TestHeartbeatSaysNothingWhenItIsStoppedMidCall(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	server, _ := console(t, func(int) error {
		cancel()
		<-time.After(50 * time.Millisecond)
		return nil
	})

	heartbeat, _, _, said := beatingInto(t, server.URL)
	heartbeat.run(ctx, make(chan struct{}))

	if said.Len() != 0 {
		t.Errorf("the connector said %q when it was stopped, want nothing", said.String())
	}
}

func TestOnlyARefusalTheConsoleAnsweredCountsAsOne(t *testing.T) {
	for name, tc := range map[string]struct {
		err  error
		want bool
	}{
		"not found":         {connect.NewError(connect.CodeNotFound, errors.New("gone")), true},
		"unauthenticated":   {connect.NewError(connect.CodeUnauthenticated, errors.New("bad token")), true},
		"unavailable":       {connect.NewError(connect.CodeUnavailable, errors.New("dial")), false},
		"deadline exceeded": {connect.NewError(connect.CodeDeadlineExceeded, errors.New("slow")), false},
		"canceled":          {connect.NewError(connect.CodeCanceled, errors.New("stopped")), false},
		"not a call":        {errors.New("sign"), false},
	} {
		if got := IsRefusal(tc.err); got != tc.want {
			t.Errorf("IsRefusal(%s) = %v, want %v", name, got, tc.want)
		}
	}
}
