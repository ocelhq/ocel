package providerserver

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	connect "connectrpc.com/connect"

	"github.com/ocelhq/ocel/pkg/channel"
	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	"github.com/ocelhq/ocel/pkg/providerkit/refusal"
)

func TestServeNeedsAConstructor(t *testing.T) {
	identity, err := channel.NewIdentity()
	if err != nil {
		t.Fatalf("NewIdentity() error = %v", err)
	}
	t.Setenv(channel.ClientCertEnvVar, identity.CertificatePEM())

	if err := Serve(Config{Version: "test"}); err == nil {
		t.Fatal("Serve() with no constructor returned nil, want a refusal to start")
	}
}

func TestServeNeedsTheClientCertificate(t *testing.T) {
	for _, tc := range []struct {
		name string
		cert string
	}{
		{"nothing at all", ""},
		{"something that is not a certificate", "not-a-pem-block"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(channel.ClientCertEnvVar, tc.cert)

			err := Serve(Config{Version: "test", New: func(context.Context, provider.Settings) (provider.Provider, error) { return nil, nil }})
			if err == nil || !strings.Contains(err.Error(), channel.ClientCertEnvVar) {
				t.Fatalf("Serve() error = %v, want it to name the environment variable the CLI must set", err)
			}
		})
	}
}

func TestSessionConstructsTheProviderExactlyOnce(t *testing.T) {
	t.Parallel()

	var built int
	s := &session{config: Config{New: func(context.Context, provider.Settings) (provider.Provider, error) {
		built++
		return stubProvider{}, nil
	}}}

	if err := s.configure(context.Background(), provider.Settings{}); err != nil {
		t.Fatalf("configure() error = %v", err)
	}
	err := s.configure(context.Background(), provider.Settings{})
	if got := connect.CodeOf(err); got != connect.CodeFailedPrecondition {
		t.Errorf("a second configure: code = %v, want %v", got, connect.CodeFailedPrecondition)
	}
	if built != 1 {
		t.Errorf("the constructor ran %d times, want exactly once per session", built)
	}
}

func TestSessionRefusesEveryRPCBeforeConfigure(t *testing.T) {
	t.Parallel()

	s := &session{config: Config{New: func(context.Context, provider.Settings) (provider.Provider, error) { return stubProvider{}, nil }}}

	_, err := s.use()
	if got := connect.CodeOf(err); got != connect.CodeFailedPrecondition {
		t.Fatalf("use() before configure: code = %v, want %v", got, connect.CodeFailedPrecondition)
	}

	if err := s.configure(context.Background(), provider.Settings{}); err != nil {
		t.Fatalf("configure() error = %v", err)
	}
	if _, err := s.use(); err != nil {
		t.Fatalf("use() after configure: error = %v", err)
	}
}

func TestSessionTurnsARefusedConstructionIntoInvalidArgument(t *testing.T) {
	t.Parallel()

	s := &session{config: Config{New: func(context.Context, provider.Settings) (provider.Provider, error) {
		return nil, refusal.Refuse(refusal.CodeInvalid, "unknown option \"regoin\"")
	}}}

	err := s.configure(context.Background(), provider.Settings{Options: provider.Options{"regoin": "typo"}})
	if got := connect.CodeOf(err); got != connect.CodeInvalidArgument {
		t.Fatalf("configure() with refused options: code = %v, want %v", got, connect.CodeInvalidArgument)
	}
	if _, err := s.use(); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Error("a refused Configure left the session usable, want it still unconfigured")
	}
}

func TestSessionReportsAFailedConstructionAsAFailure(t *testing.T) {
	t.Parallel()

	s := &session{config: Config{New: func(context.Context, provider.Settings) (provider.Provider, error) {
		return nil, errors.New("the vendor sdk is unreachable")
	}}}

	if got := connect.CodeOf(s.configure(context.Background(), provider.Settings{})); got != connect.CodeInternal {
		t.Fatalf("configure() with a failure: code = %v, want %v", got, connect.CodeInternal)
	}
}

func TestSessionRefusesAConstructorThatReturnsNothing(t *testing.T) {
	t.Parallel()

	s := &session{config: Config{New: func(context.Context, provider.Settings) (provider.Provider, error) { return nil, nil }}}

	if got := connect.CodeOf(s.configure(context.Background(), provider.Settings{})); got != connect.CodeInternal {
		t.Fatalf("configure() with a nil provider: code = %v, want %v", got, connect.CodeInternal)
	}
}

func TestSessionConfigureIsSafeUnderConcurrency(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	var built int
	s := &session{config: Config{New: func(context.Context, provider.Settings) (provider.Provider, error) {
		mu.Lock()
		defer mu.Unlock()
		built++
		return stubProvider{}, nil
	}}}

	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = s.configure(context.Background(), provider.Settings{})
		}()
	}
	wg.Wait()

	if built != 1 {
		t.Errorf("the constructor ran %d times under a concurrent Configure, want once", built)
	}
}

type stubProvider struct{ provider.Provider }

func (stubProvider) Facts() provider.Facts { return provider.Facts{Vendor: "stub"} }

func (stubProvider) Hooks() provider.Hooks { return provider.Hooks{} }
