package devserver

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"

	connect "connectrpc.com/connect"
	"connectrpc.com/validate"

	"github.com/ocelhq/ocel/cli/internal/clientenv"
	"github.com/ocelhq/ocel/cli/internal/declare"
	"github.com/ocelhq/ocel/cli/internal/discovery"
	"github.com/ocelhq/ocel/cli/internal/envgate"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/internal/resolve"
	"github.com/ocelhq/ocel/cli/internal/resourceregistry"
	"github.com/ocelhq/ocel/cli/internal/sdkversion"
	"github.com/ocelhq/ocel/cli/internal/version"
	"github.com/ocelhq/ocel/pkg/channel"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	"github.com/ocelhq/ocel/pkg/proto/app/resources/v1/resourcesv1connect"
)

type SyncResult struct {
	Resources        []resolve.Resource
	DevServerAddress string
	AppToken         string
	LiveValues       map[string]string
	LiveKeys         []string
	Err              error
}

type Stack interface {
	Resolve(ctx context.Context, resources []declare.Resource) ([]resolve.Resource, error)
	Routes(mux *http.ServeMux, guard func(http.Handler) http.Handler, options ...connect.HandlerOption)
}

type Server struct {
	registry      *resourceregistry.Registry
	stack         Stack
	devServerAddr string
	sessionToken  string
	appToken      string
	syncCh        chan SyncResult
	sdk           *sdkversion.Gate

	live   *liveKeys
	env    *envState
	fanout *envFanout
}

func New(devServerAddr string, stack Stack) *Server {
	return &Server{
		registry:      resourceregistry.New(),
		stack:         stack,
		devServerAddr: devServerAddr,
		sessionToken:  channel.NewSessionToken(),
		appToken:      channel.NewSessionToken(),
		syncCh:        make(chan SyncResult, 1),
		sdk:           sdkversion.NewGate(version.Version),
		live:          newLiveKeys(),
		env:           newEnvState(),
		fanout:        newEnvFanout(),
	}
}

func (s *Server) liveValues(keys []string) map[string]string {
	values := s.env.snapshot()
	live := make(map[string]string, len(keys))
	for _, key := range keys {
		if value, ok := values[key]; ok {
			live[key] = value
		}
	}
	return live
}

func (s *Server) Declare(_ context.Context, req *resourcesv1.DeclareRequest) (*resourcesv1.DeclareResponse, error) {
	res, err := declare.Parse(req)
	if err != nil {
		return nil, err
	}

	s.registry.Add(res)
	return &resourcesv1.DeclareResponse{}, nil
}

func (s *Server) UseValues(values map[string]string, scope envgate.Scope) {
	s.env.use(values, scope)
}

func (s *Server) DeclareEnv(ctx context.Context, req *resourcesv1.DeclareEnvRequest) (*resourcesv1.DeclareEnvResponse, error) {
	s.live.declare(req.GetDefinitions())
	return s.env.declare(ctx, req)
}

func (s *Server) CheckEnv(ctx context.Context) error {
	_, gate := s.env.current()
	if gate == nil {
		return nil
	}
	if err := gate.Prefetch(ctx); err != nil {
		return err
	}
	return gate.Check()
}

func (s *Server) ScopedFolders() map[string][]string {
	_, gate := s.env.current()
	if gate == nil {
		return nil
	}
	scoped := map[string][]string{}
	for _, definition := range gate.Definitions() {
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

func (s *Server) ReportEnvProblems(ctx context.Context, req *resourcesv1.ReportEnvProblemsRequest) (*resourcesv1.ReportEnvProblemsResponse, error) {
	if _, gate := s.env.current(); gate != nil {
		return gate.ReportEnvProblems(ctx, req)
	}
	return &resourcesv1.ReportEnvProblemsResponse{}, nil
}

func (s *Server) ResetManifest() {
	s.registry.Reset()
	s.live.reset()
	s.env.forgetDeclarations()
}

func (s *Server) SessionToken() string { return s.sessionToken }

func (s *Server) AppToken() string { return s.appToken }

func (s *Server) guard(token string, next http.Handler) http.Handler {
	return channel.LoopbackGuard(strings.TrimPrefix(s.devServerAddr, "http://"), token, next)
}

func (s *Server) Mux() *http.ServeMux {
	mux := http.NewServeMux()
	interceptors := connect.WithInterceptors(validate.NewInterceptor())
	resourcePath, resourceHandler := resourcesv1connect.NewResourceServiceHandler(s, connect.WithInterceptors(validate.NewInterceptor(), s.sdk.Interceptor()))
	mux.Handle(resourcePath, s.guard(s.sessionToken, resourceHandler))
	s.stack.Routes(mux, func(next http.Handler) http.Handler { return s.guard(s.appToken, next) }, interceptors)
	mux.Handle("/sync", s.guard(s.sessionToken, http.HandlerFunc(s.handleSync)))
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

func (s *Server) Sync() <-chan SyncResult {
	return s.syncCh
}

func (s *Server) deliverSync(res SyncResult) {
	select {
	case <-s.syncCh:
	default:
	}
	s.syncCh <- res
}

func (s *Server) handleSync(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	resolved, err := s.stack.Resolve(r.Context(), s.registry.Snapshot())
	if err != nil {
		err = fmt.Errorf("resolve resources: %w", err)
		s.deliverSync(SyncResult{Err: err})
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	liveKeys := s.live.sorted()
	s.deliverSync(SyncResult{Resources: resolved, DevServerAddress: s.devServerAddr, AppToken: s.appToken, LiveValues: s.liveValues(liveKeys), LiveKeys: liveKeys})
	w.WriteHeader(http.StatusOK)
}

func (s *Server) Discover(ctx context.Context, cfg *projectconfig.Config, stdout, stderr io.Writer) error {
	roots, err := discovery.RootsOf(cfg)
	if err != nil {
		return err
	}

	prepared, err := discovery.Prepare(cfg.Dir, roots)
	if err != nil {
		return err
	}

	_ = s.sdk.Take()
	err = discovery.Run(ctx, cfg.Dir, prepared, discovery.Server{URL: s.devServerAddr, Token: s.sessionToken}, stdout, stderr)
	if refused := s.sdk.Take(); refused != nil {
		return refused
	}
	return err
}

func (s *Server) ClientKeys() ([]clientenv.Key, error) {
	_, gate := s.env.current()
	if gate == nil {
		return nil, nil
	}
	return clientenv.Declared(gate.Definitions())
}
