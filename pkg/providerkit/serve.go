package providerkit

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"

	connect "connectrpc.com/connect"
	"connectrpc.com/validate"

	"github.com/ocelhq/ocel/pkg/channel"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	"github.com/ocelhq/ocel/pkg/proto/provider/envvars/v1/envvarsv1connect"
)

type Spec struct {
	Version string

	New func(ctx context.Context, options Options) (Provider, error)
}

func Serve(spec Spec) error {
	if spec.New == nil {
		return errors.New("providerkit: Spec.New is required")
	}

	token := os.Getenv(channel.SessionTokenEnvVar)
	if token == "" {
		return fmt.Errorf("%s must be set by the launching CLI", channel.SessionTokenEnvVar)
	}

	ln, addr, err := listen()
	if err != nil {
		return fmt.Errorf("bind provider listener: %w", err)
	}
	defer ln.Close()

	fmt.Fprintf(os.Stderr, "ocel provider %s: bound %s\n", spec.Version, addr)

	srv := &http.Server{Handler: NewMux(spec, token)}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	served := make(chan error, 1)
	go func() { served <- srv.Serve(ln) }()

	fmt.Println(channel.FormatReadinessLine(addr))

	select {
	case <-ctx.Done():
		return srv.Close()
	case err := <-served:
		if err != nil && err != http.ErrServerClosed {
			return err
		}
		return nil
	}
}

func NewMux(spec Spec, token string) *http.ServeMux {
	mux := http.NewServeMux()
	interceptors := connect.WithInterceptors(
		authInterceptor(token),
		traceInterceptor(),
		validate.NewInterceptor(),
	)

	kit := &handlers{session: &session{spec: spec, writer: WriterFor(spec.Version)}}

	path, handler := contractv1connect.NewProviderServiceHandler(kit, interceptors)
	mux.Handle(path, handler)

	path, handler = envvarsv1connect.NewEnvVarsServiceHandler(kit, interceptors)
	mux.Handle(path, handler)

	return mux
}

type handlers struct {
	session *session
}

var (
	_ contractv1connect.ProviderServiceHandler = (*handlers)(nil)
	_ envvarsv1connect.EnvVarsServiceHandler   = (*handlers)(nil)
)

type session struct {
	spec   Spec
	writer Writer

	mu       sync.Mutex
	provider Provider
}

func (s *session) configure(ctx context.Context, options Options) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.provider != nil {
		return connect.NewError(connect.CodeFailedPrecondition, errors.New("the provider session is already configured"))
	}
	provider, err := s.spec.New(ctx, options)
	if err != nil {
		return refusalError(err)
	}
	if provider == nil {
		return connect.NewError(connect.CodeInternal, errors.New("the provider constructor returned nothing"))
	}
	s.provider = provider
	return nil
}

func (s *session) use() (Provider, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.provider == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("Configure must be the first call on a provider session"))
	}
	return s.provider, nil
}
