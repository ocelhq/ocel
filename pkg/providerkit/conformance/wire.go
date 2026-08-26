package conformance

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
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
		server := httptest.NewServer(providerkit.NewMux(suite.Spec))
		t.Cleanup(server.Close)

		holdsTheSessionRules(t, client(server.Client(), server.URL), suite.Options)
	})

	t.Run("spawned", func(t *testing.T) {
		if suite.Binary == "" {
			t.Skip("the suite names no binary, so there is nothing to spawn")
		}
		provider := spawn(t, suite.Binary)

		provider.refusesAnUnpairedClient(t)
		holdsTheSessionRules(t, client(provider.http, providerURL), suite.Options)

		provider.stopsOnSIGTERM(t)
	})
}

const providerURL = "https://localhost"

func (s *spawned) refusesAnUnpairedClient(t *testing.T) {
	t.Helper()

	stranger, err := channel.NewIdentity()
	if err != nil {
		t.Fatalf("NewIdentity() error = %v", err)
	}

	for _, tc := range []struct {
		name     string
		identity *channel.Identity
		bare     bool
	}{
		{name: "a client the provider was never paired with", identity: stranger},
		{name: "a client presenting no certificate at all", identity: stranger, bare: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			config := tc.identity.ClientConfig(s.serverCert)
			if tc.bare {
				config.Certificates = nil
			}
			unpaired := client(transportFor(s.network, s.address, config), providerURL)
			if _, err := unpaired.Configure(context.Background(), &contractv1.ConfigureRequest{}); err == nil {
				t.Error("Configure() over an unpaired connection succeeded, want the provider to refuse the handshake")
			}
		})
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

func client(httpClient connect.HTTPClient, url string) contractv1connect.ProviderServiceClient {
	return contractv1connect.NewProviderServiceClient(httpClient, url)
}

type spawned struct {
	cmd  *exec.Cmd
	http *http.Client

	serverCert *x509.Certificate
	network    string
	address    string

	exit   chan error
	exited bool
	err    error
}

func spawn(t *testing.T, binary string) *spawned {
	t.Helper()

	identity, err := channel.NewIdentity()
	if err != nil {
		t.Fatalf("NewIdentity() error = %v", err)
	}
	cmd := exec.Command(binary)
	cmd.Env = append(os.Environ(), channel.ClientCertEnvVar+"="+identity.CertificatePEM())

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("StdoutPipe() error = %v", err)
	}
	stderr := &syncBuffer{}
	cmd.Stderr = stderr

	if err := cmd.Start(); err != nil {
		t.Fatalf("start %s: %v", binary, err)
	}

	provider := &spawned{cmd: cmd, exit: make(chan error, 1)}
	go func() { provider.exit <- cmd.Wait() }()
	t.Cleanup(func() {
		if provider.exited {
			return
		}
		_ = cmd.Process.Kill()
		provider.wait(readyTimeout)
	})

	type readiness struct {
		addr string
		cert *x509.Certificate
	}
	ready := make(chan readiness, 1)
	go func() {
		defer close(ready)
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			if addr, cert, ok := channel.ParseReadinessLine(scanner.Text()); ok {
				ready <- readiness{addr: addr, cert: cert}
				return
			}
		}
	}()

	select {
	case signalled, ok := <-ready:
		if !ok {
			t.Fatalf("%s exited before signalling readiness\n%s", binary, stderr.String())
		}
		network, address, err := channel.ParseAddr(signalled.addr)
		if err != nil {
			t.Fatalf("ParseAddr(%q) error = %v", signalled.addr, err)
		}
		provider.serverCert = signalled.cert
		provider.network, provider.address = network, address
		provider.http = transportFor(network, address, identity.ClientConfig(signalled.cert))
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

func transportFor(network, address string, config *tls.Config) *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			DialTLSContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				raw, err := d.DialContext(ctx, network, address)
				if err != nil {
					return nil, err
				}
				conn := tls.Client(raw, config)
				if err := conn.HandshakeContext(ctx); err != nil {
					raw.Close()
					return nil, err
				}
				return conn, nil
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
