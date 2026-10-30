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
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	connect "connectrpc.com/connect"
	"connectrpc.com/validate"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/localrpc"
	"github.com/ocelhq/ocel/pkg/processenv"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	"github.com/ocelhq/ocel/pkg/proto/provider/cost/v1/costv1connect"
	"github.com/ocelhq/ocel/pkg/proto/provider/variablestore/v1/variablestorev1connect"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/bootstrapplan"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/variablestoreserver"
)

type Config struct {
	Version string

	New func(ctx context.Context, settings provider.Settings) (provider.Provider, error)

	ExportSpans func(ctx context.Context, service string) (context.Context, func(context.Context) error, error)

	Slug            string
	ServesSlugAlone bool
	Tier            environment.Tier
}

func Serve(config Config) error {
	if config.New == nil {
		return errors.New("providerserver: Config.New is required")
	}
	if config.ExportSpans == nil {
		return errors.New("providerserver: Config.ExportSpans is required")
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

	ctx, stopExporting, err := config.ExportSpans(ctx, filepath.Base(os.Args[0]))
	if err != nil {
		return fmt.Errorf("export provider spans: %w", err)
	}
	defer func() {
		flushed, cancel := context.WithTimeout(context.Background(), spanFlushWindow)
		defer cancel()
		_ = stopExporting(flushed)
	}()

	return serve(cancelWhenStdinCloses(ctx, os.Stdin), ln, NewSession(config).Handler(), shutdownWindow, func() {
		fmt.Println(localrpc.FormatReadinessLine(config.Version, addr, identity.CertificateDER()))
	})
}

const (
	shutdownWindow  = 10 * time.Second
	spanFlushWindow = 2 * time.Second
)

func serve(ctx context.Context, ln net.Listener, handler http.Handler, window time.Duration, announce func()) error {
	calls, cancelCalls := context.WithCancel(context.WithoutCancel(ctx))
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

func ConformanceMux(config Config) http.Handler { return NewSession(config).Handler() }

type Session struct {
	handlers *handlers
	mux      *http.ServeMux
}

func NewSession(config Config) *Session {
	mux := http.NewServeMux()
	asked := newQuestions()
	interceptors := connect.WithInterceptors(
		callSpanInterceptor{},
		validate.NewInterceptor(),
		servedProject{alone: config.ServesSlugAlone, slug: config.Slug, tier: config.Tier},
		asked,
	)

	s := &session{config: config, writer: provider.WrittenByVersion(config.Version)}
	services := &handlers{session: s, questions: asked, leases: newSubjectLeases(), skipChecks: processenv.SkipChecks(), Service: &variablestoreserver.Service{Source: sessionBackend{session: s}}}

	path, handler := contractv1connect.NewProviderServiceHandler(services, interceptors)
	mux.Handle(path, handler)

	path, handler = variablestorev1connect.NewVariableStoreServiceHandler(services, interceptors)
	mux.Handle(path, handler)

	path, handler = costv1connect.NewCostServiceHandler(services, interceptors)
	mux.Handle(path, handler)

	return &Session{handlers: services, mux: mux}
}

func (s *Session) Handler() http.Handler { return s.mux }

func (s *Session) Configure(ctx context.Context, settings provider.Settings, edge *contractv1.EdgeSelection) (*contractv1.ProviderFacts, error) {
	return s.handlers.configure(ctx, settings, edge)
}

func (s *Session) Confirm(ctx context.Context, questionID string) error {
	return s.handlers.questions.confirm(ctx, questionID)
}

func (s *Session) ListPendingQuestions() []PendingQuestion {
	return s.handlers.questions.listPending()
}

type handlers struct {
	*variablestoreserver.Service

	session   *session
	questions *questions
	forwards  openForwards
	leases    *subjectLeases

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

func (s sessionBackend) Read(context.Context) (variablestoreserver.Backend, error) {
	vendor, err := s.session.use()
	if err != nil {
		return variablestoreserver.Backend{}, err
	}
	hooks := vendor.Hooks()
	backend := variablestoreserver.Backend{
		KeyValues: vendor.KeyValues(),
		Cipher:    vendor.Cipher(),
	}
	if hooks.EnvSource != nil {
		backend.ProveIdentity, backend.EnsureEnvSourceSync = hooks.EnvSource.ProveIdentity, hooks.EnvSource.EnsureSync
	}
	return backend, nil
}

type session struct {
	config Config
	writer provider.WrittenBy

	mu        sync.Mutex
	provider  provider.Provider
	settings  provider.Settings
	namespace provider.Namespace

	bootstrapStatuses bootstrapplan.Statuses
	previewKeys       openedPreviewKeys
}

func (s *session) refuseOtherTier(acted environment.Tier) error {
	if s.config.Tier == "" || acted == s.config.Tier {
		return nil
	}
	return errOtherTier(s.config.Tier, acted)
}

func (s *session) transforms() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.settings.Transforms
}

func (s *session) options() provider.Options {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.settings.Options
}

func (s *session) configure(ctx context.Context, settings provider.Settings) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.provider != nil {
		return connect.NewError(connect.CodeFailedPrecondition, errors.New("the provider session is already configured"))
	}
	if !filepath.IsAbs(settings.ProjectDir) {
		return provider.RefusalError(refusal.Refuse(refusal.CodeInvalid,
			"the provider was configured with the project directory %q, which is not absolute: configure it with the project's absolute path", settings.ProjectDir))
	}
	namespace, err := provider.NamespaceFromEnv()
	if err != nil {
		return provider.RefusalError(err)
	}
	p, err := s.config.New(ctx, settings)
	if err != nil {
		return provider.RefusalError(err)
	}
	if p == nil {
		return connect.NewError(connect.CodeInternal, errors.New("the provider constructor returned nothing"))
	}
	if err := refuseWebIdentity(settings.WebIdentity, p.Facts()); err != nil {
		return provider.RefusalError(err)
	}
	s.provider = p
	s.settings = settings
	s.namespace = namespace
	return nil
}

func (s *session) managedBy() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return string(s.namespace)
}

func (s *session) deploysWithWebIdentity() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.settings.WebIdentity != nil
}

func (s *session) use() (provider.Provider, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.provider == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("Configure must be the first call on a provider session"))
	}
	return s.provider, nil
}

func refuseWebIdentity(identity *provider.WebIdentity, facts provider.Facts) error {
	if identity == nil {
		return nil
	}
	if facts.WebIdentity == nil {
		return refusal.Refuse(refusal.CodeInvalid,
			"provider %s deploys with no web identity: deploy with the credentials its environment names", facts.Vendor)
	}
	var named []string
	for _, name := range facts.WebIdentity.CredentialEnvVars {
		if os.Getenv(name) != "" {
			named = append(named, name)
		}
	}
	if len(named) > 0 {
		return refusal.Refuse(refusal.CodeInvalid,
			"this run names its own credential in %s and was handed a web identity too: deploy with one of them", strings.Join(named, ", "))
	}
	return nil
}
