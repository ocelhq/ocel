package providerserver

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	connect "connectrpc.com/connect"
	"connectrpc.com/validate"

	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/localrpc"
	"github.com/ocelhq/ocel/pkg/processenv"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	"github.com/ocelhq/ocel/pkg/proto/provider/cost/v1/costv1connect"
	"github.com/ocelhq/ocel/pkg/proto/provider/variablestore/v1/variablestorev1connect"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/variablestoreserver"
)

type Config struct {
	Version string

	New func(ctx context.Context, settings provider.Settings) (provider.Provider, error)
}

func Serve(config Config) error {
	if config.New == nil {
		return errors.New("providerserver: Config.New is required")
	}

	bound, addr, err := listen()
	if err != nil {
		return fmt.Errorf("bind provider listener: %w", err)
	}
	ln, identity, err := localrpc.SecureListener(bound)
	if err != nil {
		bound.Close()
		return err
	}
	defer ln.Close()

	if _, debug := os.LookupEnv("OCEL_PROVIDER_DEBUG"); debug {
		fmt.Fprintf(os.Stderr, "ocel provider %s: bound %s\n", config.Version, addr)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	return serve(cancelWhenStdinCloses(ctx, os.Stdin), ln, newMux(config), shutdownWindow, func() {
		fmt.Println(localrpc.FormatReadinessLine(config.Version, addr, identity.CertificateDER()))
	})
}

const shutdownWindow = 10 * time.Second

func serve(ctx context.Context, ln net.Listener, handler http.Handler, window time.Duration, announce func()) error {
	calls, cancelCalls := context.WithCancel(context.Background())
	defer cancelCalls()
	srv := &http.Server{Handler: handler, BaseContext: func(net.Listener) context.Context { return calls }}

	served := make(chan error, 1)
	go func() { served <- srv.Serve(ln) }()

	announce()

	select {
	case <-ctx.Done():
	case err := <-served:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	}

	cancelCalls()
	drained, cancel := context.WithTimeout(context.Background(), window)
	defer cancel()
	if err := srv.Shutdown(drained); err != nil {
		return srv.Close()
	}
	return nil
}

func cancelWhenStdinCloses(ctx context.Context, stdin *os.File) context.Context {
	info, err := stdin.Stat()
	if err != nil || info.Mode()&os.ModeNamedPipe == 0 {
		return ctx
	}
	ctx, cancel := context.WithCancel(ctx)
	go func() {
		defer cancel()
		_, _ = io.Copy(io.Discard, stdin)
	}()
	return ctx
}

func ConformanceMux(config Config) *http.ServeMux { return newMux(config) }

func newMux(config Config) *http.ServeMux {
	mux := http.NewServeMux()
	asked := newQuestions()
	interceptors := connect.WithInterceptors(
		traceInterceptor(),
		validate.NewInterceptor(),
		asked,
	)

	s := &session{config: config, writer: provider.WrittenByVersion(config.Version)}
	services := &handlers{session: s, questions: asked, leases: newEnvironmentLeases(), skipChecks: processenv.SkipChecks(), Service: &variablestoreserver.Service{Source: sessionBackend{session: s}, CallerNamesEnvSource: true}}

	path, handler := contractv1connect.NewProviderServiceHandler(services, interceptors)
	mux.Handle(path, handler)

	path, handler = variablestorev1connect.NewVariableStoreServiceHandler(services, interceptors)
	mux.Handle(path, handler)

	path, handler = costv1connect.NewCostServiceHandler(services, interceptors)
	mux.Handle(path, handler)

	return mux
}

type handlers struct {
	*variablestoreserver.Service

	session   *session
	questions *questions
	forwards  openForwards
	leases    *environmentLeases

	skipChecks bool
}

var (
	_ contractv1connect.ProviderServiceHandler           = (*handlers)(nil)
	_ variablestorev1connect.VariableStoreServiceHandler = (*handlers)(nil)
	_ costv1connect.CostServiceHandler                   = (*handlers)(nil)
)

type sessionBackend struct {
	session *session
}

func (s sessionBackend) Read() (variablestoreserver.Backend, error) {
	vendor, err := s.session.use()
	if err != nil {
		return variablestoreserver.Backend{}, err
	}
	hooks := vendor.Hooks()
	return variablestoreserver.Backend{
		KeyValues:     vendor.KeyValues(),
		Cipher:        vendor.Cipher(),
		VerifyGrants:  hooks.VerifyGrants,
		ProveIdentity: hooks.ProveIdentity,
	}, nil
}

type session struct {
	config Config
	writer provider.WrittenBy

	mu         sync.Mutex
	provider   provider.Provider
	settings   provider.Settings
	outputRoot string

	bootstrapStatuses BootstrapStatuses
	previewKeys       openedPreviewKeys
}

func (s *session) transforms() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.settings.Transforms
}

func (s *session) artifactRoot() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.outputRoot
}

func (s *session) configure(ctx context.Context, settings provider.Settings) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.provider != nil {
		return connect.NewError(connect.CodeFailedPrecondition, errors.New("the provider session is already configured"))
	}
	outputRoot, err := buildoutput.Root(settings.ProjectDir)
	if err != nil {
		return provider.RefusalError(refusal.Refuse(refusal.CodeInvalid,
			"the provider was configured with the project directory %q, and it reads the build under that directory: configure it with the project's absolute path", settings.ProjectDir))
	}
	p, err := s.config.New(ctx, settings)
	if err != nil {
		return provider.RefusalError(err)
	}
	if p == nil {
		return connect.NewError(connect.CodeInternal, errors.New("the provider constructor returned nothing"))
	}
	s.provider = p
	s.settings = settings
	s.outputRoot = outputRoot
	return nil
}

func (s *session) use() (provider.Provider, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.provider == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("Configure must be the first call on a provider session"))
	}
	return s.provider, nil
}
