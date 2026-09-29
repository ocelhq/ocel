package clitest

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/version"
	"github.com/ocelhq/ocel/pkg/localrpc"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/provider/providerserver"
)

const fakeSessionsEnvVar = "OCEL_TEST_FAKE_SESSIONS"

func IsFakeSession() bool {
	return os.Getenv(fakeSessionsEnvVar) != "" && os.Getenv(localrpc.ClientCertEnvVar) != ""
}

func RunFakeSession() int {
	control := os.Getenv(fakeSessionsEnvVar)
	client := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", control)
		},
	}}
	resp, err := client.Post("http://fake/session", "application/x-pem-file", strings.NewReader(os.Getenv(localrpc.ClientCertEnvVar)))
	if err != nil {
		fmt.Fprintln(os.Stderr, "fake provider: open a session:", err)
		return 1
	}
	line, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil || resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "fake provider: open a session: %s %v\n", line, err)
		return 1
	}
	fmt.Println(string(line))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	<-ctx.Done()
	return 0
}

func ServeFake(t *testing.T, p *fake.Provider) *ProviderRequests {
	t.Helper()

	dir, err := os.MkdirTemp("", "ocel-fake-")
	if err != nil {
		t.Fatalf("reserve the fake provider's socket dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })

	controlPath := filepath.Join(dir, "control.sock")
	control, err := net.Listen("unix", controlPath)
	if err != nil {
		t.Fatalf("listen for fake provider sessions: %v", err)
	}
	sessions := &fakeSessions{dir: dir, provider: p, requests: &ProviderRequests{}}
	server := &http.Server{Handler: http.HandlerFunc(sessions.open)}
	go server.Serve(control)
	t.Cleanup(func() {
		server.Close()
		sessions.close()
	})

	testBinary, err := filepath.Abs(os.Args[0])
	if err != nil {
		t.Fatalf("resolve test binary path: %v", err)
	}
	InstallProvider(t, string(fake.Vendor), func(dest string) error { return os.Symlink(testBinary, dest) })
	t.Setenv(fakeSessionsEnvVar, controlPath)
	return sessions.requests
}

type fakeSessions struct {
	dir      string
	provider *fake.Provider
	requests *ProviderRequests

	mu      sync.Mutex
	servers []*http.Server
}

func (s *fakeSessions) open(w http.ResponseWriter, r *http.Request) {
	encoded, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	line, err := s.serve(string(encoded))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	fmt.Fprint(w, line)
}

func (s *fakeSessions) serve(clientPEM string) (string, error) {
	client, err := localrpc.ParseCertificatePEM(clientPEM)
	if err != nil {
		return "", err
	}
	identity, err := localrpc.NewIdentity()
	if err != nil {
		return "", err
	}
	config, err := identity.ServerConfig(client)
	if err != nil {
		return "", err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	path := filepath.Join(s.dir, fmt.Sprintf("session-%d.sock", len(s.servers)))
	ln, err := net.Listen("unix", path)
	if err != nil {
		return "", err
	}
	server := &http.Server{Handler: s.requests.record(providerserver.ConformanceMux(providerserver.Config{
		Version: version.Version,
		New: func(context.Context, provider.Settings) (provider.Provider, error) {
			return s.provider, nil
		},
	}))}
	s.servers = append(s.servers, server)
	go func() {
		if err := server.Serve(tls.NewListener(ln, config)); err != nil && !errors.Is(err, http.ErrServerClosed) {
			fmt.Fprintln(os.Stderr, "fake provider session:", err)
		}
	}()
	return localrpc.FormatReadinessLine(version.Version, localrpc.FormatUnixAddress(path), identity.CertificateDER()), nil
}

func (s *fakeSessions) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, server := range s.servers {
		server.Close()
	}
}
