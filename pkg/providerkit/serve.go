package providerkit

import (
	"context"
	"crypto/tls"
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

	encoded := os.Getenv(channel.ClientCertEnvVar)
	if encoded == "" {
		return fmt.Errorf("%s must be set by the launching CLI", channel.ClientCertEnvVar)
	}
	clientCert, err := channel.ParseCertificatePEM(encoded)
	if err != nil {
		return fmt.Errorf("%s does not carry a certificate: %w", channel.ClientCertEnvVar, err)
	}

	identity, err := channel.NewIdentity()
	if err != nil {
		return fmt.Errorf("mint the provider channel identity: %w", err)
	}

	ln, addr, err := listen()
	if err != nil {
		return fmt.Errorf("bind provider listener: %w", err)
	}
	ln = tls.NewListener(ln, identity.ServerConfig(clientCert))
	defer ln.Close()

	if _, debug := os.LookupEnv("OCEL_PROVIDER_DEBUG"); debug {
		fmt.Fprintf(os.Stderr, "ocel provider %s: bound %s\n", spec.Version, addr)
	}

	srv := &http.Server{Handler: NewMux(spec)}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	served := make(chan error, 1)
	go func() { served <- srv.Serve(ln) }()

	fmt.Println(channel.FormatReadinessLine(addr, identity.CertificateDER()))

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

func NewMux(spec Spec) *http.ServeMux {
	mux := http.NewServeMux()
	interceptors := connect.WithInterceptors(
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
		return RefusalError(err)
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
