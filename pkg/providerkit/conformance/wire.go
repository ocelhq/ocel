package conformance

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"testing"
	"time"

	connect "connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/ocelhq/ocel/pkg/channel"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	"github.com/ocelhq/ocel/pkg/providerkit"
)

const unknownOption = "an-option-no-provider-accepts"

const readyTimeout = 20 * time.Second

func runWire(t *testing.T, suite Suite) {
	t.Helper()

	t.Run("in process", func(t *testing.T) {
		if suite.Spec.New == nil {
			t.Skip("the suite carries no Spec, so there is no mux to serve")
		}
		token := newToken(t)
		server := httptest.NewServer(providerkit.NewMux(suite.Spec, token))
		t.Cleanup(server.Close)

		rejectsAnUnknownToken(t, client(server.Client(), server.URL, "not-the-token"))
		holdsTheSessionRules(t, client(server.Client(), server.URL, token), suite.Options)
	})

	t.Run("spawned", func(t *testing.T) {
		if suite.Binary == "" {
			t.Skip("the suite names no binary, so there is nothing to spawn")
		}
		provider := spawn(t, suite.Binary)

		rejectsAnUnknownToken(t, client(provider.http, "http://provider", "not-the-token"))
		holdsTheSessionRules(t, client(provider.http, "http://provider", provider.token), suite.Options)

		provider.stopsOnSIGTERM(t)
	})
}

func rejectsAnUnknownToken(t *testing.T, provider contractv1connect.ProviderServiceClient) {
	t.Helper()
	_, err := provider.Configure(context.Background(), &contractv1.ConfigureRequest{})
	if got := connect.CodeOf(err); got != connect.CodeUnauthenticated {
		t.Errorf("Configure() with the wrong token: code = %v, want %v", got, connect.CodeUnauthenticated)
	}
}

func holdsTheSessionRules(t *testing.T, provider contractv1connect.ProviderServiceClient, options providerkit.Options) {
	t.Helper()
	ctx := context.Background()

	_, err := provider.ListEnvironments(ctx, &contractv1.ListEnvironmentsRequest{Slug: "conformance"})
	if got := connect.CodeOf(err); got != connect.CodeFailedPrecondition {
		t.Errorf("an RPC before Configure: code = %v, want %v", got, connect.CodeFailedPrecondition)
	}

	refused := providerkit.Options{unknownOption: true}
	_, err = provider.Configure(ctx, configureWith(t, refused))
	if got := connect.CodeOf(err); got != connect.CodeInvalidArgument {
		t.Errorf("Configure() with an unknown option: code = %v, want %v", got, connect.CodeInvalidArgument)
	}

	if _, err := provider.Configure(ctx, configureWith(t, options)); err != nil {
		t.Fatalf("Configure() error = %v, want the session configured", err)
	}

	_, err = provider.Configure(ctx, configureWith(t, options))
	if got := connect.CodeOf(err); got != connect.CodeFailedPrecondition {
		t.Errorf("a second Configure: code = %v, want %v", got, connect.CodeFailedPrecondition)
	}

	_, err = provider.ListEnvironments(ctx, &contractv1.ListEnvironmentsRequest{Slug: "conformance"})
	if got := connect.CodeOf(err); got == connect.CodeFailedPrecondition {
		t.Errorf("an RPC after Configure: code = %v, want the session to be past its precondition", got)
	}
}

func configureWith(t *testing.T, options providerkit.Options) *contractv1.ConfigureRequest {
	t.Helper()
	if options == nil {
		return &contractv1.ConfigureRequest{Config: &contractv1.ProviderConfig{}}
	}
	fields, err := structpb.NewStruct(options)
	if err != nil {
		t.Fatalf("structpb.NewStruct(%v) error = %v", options, err)
	}
	return &contractv1.ConfigureRequest{Config: &contractv1.ProviderConfig{Options: fields}}
}

func client(httpClient connect.HTTPClient, url, token string) contractv1connect.ProviderServiceClient {
	return contractv1connect.NewProviderServiceClient(httpClient, url, connect.WithInterceptors(bearer(token)))
}

func bearer(token string) connect.Interceptor {
	return connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			req.Header().Set("Authorization", channel.FormatAuthHeader(token))
			return next(ctx, req)
		}
	})
}

type spawned struct {
	cmd   *exec.Cmd
	token string
	http  *http.Client

	exit   chan error
	exited bool
	err    error
}

func spawn(t *testing.T, binary string) *spawned {
	t.Helper()

	token := newToken(t)
	cmd := exec.Command(binary)
	cmd.Env = append(os.Environ(), channel.SessionTokenEnvVar+"="+token)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("StdoutPipe() error = %v", err)
	}
	stderr := &syncBuffer{}
	cmd.Stderr = stderr

	if err := cmd.Start(); err != nil {
		t.Fatalf("start %s: %v", binary, err)
	}

	provider := &spawned{cmd: cmd, token: token, exit: make(chan error, 1)}
	go func() { provider.exit <- cmd.Wait() }()
	t.Cleanup(func() {
		if provider.exited {
			return
		}
		_ = cmd.Process.Kill()
		provider.wait(readyTimeout)
	})

	addrs := make(chan string, 1)
	go func() {
		defer close(addrs)
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			if addr, ok := channel.ParseReadinessLine(scanner.Text()); ok {
				addrs <- addr
				return
			}
		}
	}()

	select {
	case addr, ok := <-addrs:
		if !ok {
			t.Fatalf("%s exited before signalling readiness\n%s", binary, stderr.String())
		}
		provider.http = dial(t, addr)
	case <-time.After(readyTimeout):
		t.Fatalf("%s did not signal readiness within %s", binary, readyTimeout)
	}

	return provider
}

func (s *spawned) wait(timeout time.Duration) bool {
	if s.exited {
		return true
	}
	select {
	case s.err = <-s.exit:
		s.exited = true
		return true
	case <-time.After(timeout):
		return false
	}
}

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func dial(t *testing.T, addr string) *http.Client {
	t.Helper()
	network, address, err := channel.ParseAddr(addr)
	if err != nil {
		t.Fatalf("ParseAddr(%q) error = %v", addr, err)
	}
	return &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, network, address)
			},
		},
	}
}

func (s *spawned) stopsOnSIGTERM(t *testing.T) {
	t.Helper()
	if err := s.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("signal the provider: %v", err)
	}
	if !s.wait(readyTimeout) {
		t.Errorf("the provider did not exit within %s of SIGTERM", readyTimeout)
		return
	}
	if s.err != nil {
		t.Errorf("the provider exited %v on SIGTERM, want a clean stop", s.err)
	}
}

func newToken(t *testing.T) string {
	t.Helper()
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		t.Fatalf("mint a session token: %v", err)
	}
	return hex.EncodeToString(raw)
}
