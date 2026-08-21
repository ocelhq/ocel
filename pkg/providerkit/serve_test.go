package providerkit

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	connect "connectrpc.com/connect"

	"github.com/ocelhq/ocel/pkg/channel"
)

func TestServeNeedsAConstructor(t *testing.T) {
	t.Setenv(channel.SessionTokenEnvVar, "a-token")

	if err := Serve(Spec{Version: "test"}); err == nil {
		t.Fatal("Serve() with no constructor returned nil, want a refusal to start")
	}
}

func TestServeNeedsTheSessionToken(t *testing.T) {
	t.Setenv(channel.SessionTokenEnvVar, "")

	err := Serve(Spec{Version: "test", New: func(context.Context, Options) (Provider, error) { return nil, nil }})
	if err == nil || !strings.Contains(err.Error(), channel.SessionTokenEnvVar) {
		t.Fatalf("Serve() error = %v, want it to name the environment variable the CLI must set", err)
	}
}

func TestSessionConstructsTheProviderExactlyOnce(t *testing.T) {
	t.Parallel()

	var built int
	s := &session{spec: Spec{New: func(context.Context, Options) (Provider, error) {
		built++
		return stubProvider{}, nil
	}}}

	if err := s.configure(context.Background(), nil); err != nil {
		t.Fatalf("configure() error = %v", err)
	}
	err := s.configure(context.Background(), nil)
	if got := connect.CodeOf(err); got != connect.CodeFailedPrecondition {
		t.Errorf("a second configure: code = %v, want %v", got, connect.CodeFailedPrecondition)
	}
	if built != 1 {
		t.Errorf("the constructor ran %d times, want exactly once per session", built)
	}
}

func TestSessionRefusesEveryRPCBeforeConfigure(t *testing.T) {
	t.Parallel()

	s := &session{spec: Spec{New: func(context.Context, Options) (Provider, error) { return stubProvider{}, nil }}}

	_, err := s.use()
	if got := connect.CodeOf(err); got != connect.CodeFailedPrecondition {
		t.Fatalf("use() before configure: code = %v, want %v", got, connect.CodeFailedPrecondition)
	}

	if err := s.configure(context.Background(), nil); err != nil {
		t.Fatalf("configure() error = %v", err)
	}
	if _, err := s.use(); err != nil {
		t.Fatalf("use() after configure: error = %v", err)
	}
}

func TestSessionTurnsARefusedConstructionIntoInvalidArgument(t *testing.T) {
	t.Parallel()

	s := &session{spec: Spec{New: func(context.Context, Options) (Provider, error) {
		return nil, Refuse(CodeInvalid, "unknown option \"regoin\"")
	}}}

	err := s.configure(context.Background(), Options{"regoin": "typo"})
	if got := connect.CodeOf(err); got != connect.CodeInvalidArgument {
		t.Fatalf("configure() with refused options: code = %v, want %v", got, connect.CodeInvalidArgument)
	}
	if _, err := s.use(); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Error("a refused Configure left the session usable, want it still unconfigured")
	}
}

func TestSessionReportsAFailedConstructionAsAFailure(t *testing.T) {
	t.Parallel()

	s := &session{spec: Spec{New: func(context.Context, Options) (Provider, error) {
		return nil, errors.New("the vendor sdk is unreachable")
	}}}

	if got := connect.CodeOf(s.configure(context.Background(), nil)); got != connect.CodeInternal {
		t.Fatalf("configure() with a failure: code = %v, want %v", got, connect.CodeInternal)
	}
}

func TestSessionRefusesAConstructorThatReturnsNothing(t *testing.T) {
	t.Parallel()

	s := &session{spec: Spec{New: func(context.Context, Options) (Provider, error) { return nil, nil }}}

	if got := connect.CodeOf(s.configure(context.Background(), nil)); got != connect.CodeInternal {
		t.Fatalf("configure() with a nil provider: code = %v, want %v", got, connect.CodeInternal)
	}
}

func TestSessionConfigureIsSafeUnderConcurrency(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	var built int
	s := &session{spec: Spec{New: func(context.Context, Options) (Provider, error) {
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
			_ = s.configure(context.Background(), nil)
		}()
	}
	wg.Wait()

	if built != 1 {
		t.Errorf("the constructor ran %d times under a concurrent Configure, want once", built)
	}
}

type stubProvider struct{ Provider }

func (stubProvider) Vendor() Vendor { return "stub" }
