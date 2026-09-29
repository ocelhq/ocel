package devserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"

	connect "connectrpc.com/connect"
	"connectrpc.com/validate"

	"github.com/ocelhq/ocel/cli/internal/clientenv"
	"github.com/ocelhq/ocel/cli/internal/declaration"
	"github.com/ocelhq/ocel/cli/internal/devresources/binding"
	"github.com/ocelhq/ocel/cli/internal/discovery"
	"github.com/ocelhq/ocel/cli/internal/variables"
	"github.com/ocelhq/ocel/pkg/localrpc"
)

type SyncResult struct {
	Resources    []binding.Resolved
	DevServerURL string
	AppToken     string
	SecretValues map[string]string
	SecretKeys   []string
	Err          error
}

type Resources interface {
	Resolve(ctx context.Context, resources []declaration.Resource) ([]binding.Resolved, error)
	Routes(mux *http.ServeMux, guard func(http.Handler) http.Handler, options ...connect.HandlerOption)
}

type Server struct {
	declarations   *declaration.Service
	resources      Resources
	url            string
	discoveryToken string
	appToken       string
	syncResults    chan SyncResult

	env    *envValues
	fanout *envFanout
}

func New(url string, resources Resources) *Server {
	env := newEnvValues()
	return &Server{
		declarations:   declaration.NewService(env),
		resources:      resources,
		url:            url,
		discoveryToken: localrpc.NewSessionToken(),
		appToken:       localrpc.NewSessionToken(),
		syncResults:    make(chan SyncResult, 1),
		env:            env,
		fanout:         newEnvFanout(),
	}
}

func (s *Server) secretValues(keys []string) map[string]string {
	values := s.env.snapshot()
	secrets := make(map[string]string, len(keys))
	for _, key := range keys {
		if value, ok := values[key]; ok {
			secrets[key] = value
		}
	}
	return secrets
}

func (s *Server) UseValues(values map[string]string, scope variables.Scope) {
	s.env.use(values, scope)
}

func (s *Server) RefuseIncompleteEnv(ctx context.Context) error {
	_, declarations := s.env.current()
	if declarations == nil {
		return nil
	}
	if err := declarations.Prefetch(ctx); err != nil {
		return err
	}
	return declarations.RefuseIncomplete()
}

func (s *Server) ScopedFolders() map[string][]string {
	_, declarations := s.env.current()
	if declarations == nil {
		return nil
	}
	scoped := map[string][]string{}
	for _, definition := range declarations.Definitions() {
		folders := definition.GetFolders()
		if len(folders) == 0 {
			continue
		}
		key := definition.GetKey()
		for _, folder := range folders {
			if !slices.Contains(scoped[key], folder) {
				scoped[key] = append(scoped[key], folder)
			}
		}
	}
	for key := range scoped {
		slices.Sort(scoped[key])
	}
	return scoped
}

func (s *Server) ResetDeclarations() {
	s.declarations.ForgetResources()
	s.env.forgetDeclarations()
}

func (s *Server) DiscoveryTarget() discovery.Server {
	return discovery.Server{URL: s.url, Token: s.discoveryToken}
}

func (s *Server) TakeSDKRefusal() error { return s.declarations.TakeSDKRefusal() }

func (s *Server) AppToken() string { return s.appToken }

func (s *Server) guard(token string, next http.Handler) http.Handler {
	return localrpc.LoopbackGuard(strings.TrimPrefix(s.url, "http://"), token, next)
}

func (s *Server) Mux() *http.ServeMux {
	mux := http.NewServeMux()
	interceptors := connect.WithInterceptors(validate.NewInterceptor())
	resourcePath, resourceHandler := s.declarations.Handler()
	mux.Handle(resourcePath, s.guard(s.discoveryToken, resourceHandler))
	s.resources.Routes(mux, func(next http.Handler) http.Handler { return s.guard(s.appToken, next) }, interceptors)
	mux.Handle("/sync", s.guard(s.discoveryToken, http.HandlerFunc(s.handleSync)))
	mux.Handle("/env", s.guard(s.appToken, http.HandlerFunc(s.handleEnv)))
	return mux
}

func (s *Server) PushEnv(env map[string]string) {
	s.fanout.push(env)
}

func (s *Server) handleEnv(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	ch := s.fanout.subscribe()
	defer s.fanout.unsubscribe(ch)

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case env := <-ch:
			payload, err := json.Marshal(env)
			if err != nil {
				return
			}
			if _, err := fmt.Fprintf(w, "data: %s\n\n", payload); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func (s *Server) SyncResults() <-chan SyncResult {
	return s.syncResults
}

func (s *Server) deliverSync(res SyncResult) {
	select {
	case <-s.syncResults:
	default:
	}
	s.syncResults <- res
}

func (s *Server) handleSync(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	resolved, err := s.resources.Resolve(r.Context(), s.declarations.Resources())
	if err != nil {
		err = fmt.Errorf("resolve resources: %w", err)
		s.deliverSync(SyncResult{Err: err})
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	secretKeys := s.env.secretKeys()
	s.deliverSync(SyncResult{Resources: resolved, DevServerURL: s.url, AppToken: s.appToken, SecretValues: s.secretValues(secretKeys), SecretKeys: secretKeys})
	w.WriteHeader(http.StatusOK)
}

func (s *Server) ClientKeys() ([]clientenv.Key, error) {
	_, declarations := s.env.current()
	if declarations == nil {
		return nil, nil
	}
	return clientenv.Declared(declarations.Definitions())
}
