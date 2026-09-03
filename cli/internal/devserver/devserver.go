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
	"github.com/ocelhq/ocel/cli/internal/console/blob"
	"github.com/ocelhq/ocel/cli/internal/console/resolver"
	"github.com/ocelhq/ocel/cli/internal/declare"
	"github.com/ocelhq/ocel/cli/internal/discovery"
	"github.com/ocelhq/ocel/cli/internal/envgate"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/internal/resolve"
	"github.com/ocelhq/ocel/cli/internal/resourceregistry"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/proto/app/blob/v1/blobv1connect"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	"github.com/ocelhq/ocel/pkg/proto/app/resources/v1/resourcesv1connect"
	linksv1 "github.com/ocelhq/ocel/pkg/proto/common/links/v1"
	"google.golang.org/protobuf/encoding/protojson"
)

type SyncResult struct {
	Account          resolve.Account
	Resources        []resolve.Resource
	DevServerAddress string
	LiveValues       map[string]string
	LiveKeys         []string
	Err              error
}

type Server struct {
	registry      *resourceregistry.Registry
	apiURL        string
	token         string
	projectID     string
	devServerAddr string
	blob          *blob.Proxy
	detector      *blob.Detector
	syncCh        chan SyncResult

	fetchAccount    func(ctx context.Context, apiURL, token, projectID string) (resolve.Account, error)
	resolve         func(ctx context.Context, cfg resolve.Account, resources []resourceregistry.Entry) ([]resolve.Resource, error)
	fetchLiveValues func(ctx context.Context, apiURL, token, projectID string, keys []string) (map[string]string, error)

	config *configCache
	live   *liveKeys
	env    *envState
	fanout *envFanout
}

func New(apiURL, token, projectID, devServerAddr string) *Server {
	return &Server{
		registry:        resourceregistry.New(),
		apiURL:          apiURL,
		token:           token,
		projectID:       projectID,
		devServerAddr:   devServerAddr,
		blob:            blob.NewProxy(apiURL, token, projectID),
		detector:        blob.NewDetector(apiURL, token, projectID),
		syncCh:          make(chan SyncResult, 1),
		fetchAccount:    resolve.StubAccount,
		resolve:         resolver.Resolve,
		fetchLiveValues: resolve.StubLiveValues,
		config:          newConfigCache(),
		live:            newLiveKeys(),
		env:             newEnvState(),
		fanout:          newEnvFanout(),
	}
}

func (s *Server) RunDetector(ctx context.Context, reportErr func(error)) {
	s.detector.Run(ctx, reportErr)
}

func (s *Server) Declare(_ context.Context, req *resourcesv1.DeclareRequest) (*resourcesv1.DeclareResponse, error) {
	res, err := declare.Parse(req)
	if err != nil {
		return nil, err
	}

	s.registry.Add(resourceregistry.Entry{Name: res.Name, Type: res.Type})
	return &resourcesv1.DeclareResponse{}, nil
}

func (s *Server) UseValues(values map[string]string, scope envgate.Scope) {
	s.env.use(values, scope)
}

func (s *Server) UseAccount(cfg resolve.Account) {
	s.config.use(cfg)
}

func (s *Server) account(ctx context.Context) (resolve.Account, error) {
	if cfg, ok := s.config.held(); ok {
		return cfg, nil
	}
	return s.fetchAccount(ctx, s.apiURL, s.token, s.projectID)
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

func (s *Server) Mux() *http.ServeMux {
	mux := http.NewServeMux()
	interceptors := connect.WithInterceptors(validate.NewInterceptor())
	resourcePath, resourceHandler := resourcesv1connect.NewResourceServiceHandler(s, interceptors)
	mux.Handle(resourcePath, resourceHandler)
	blobPath, blobHandler := blobv1connect.NewBucketServiceHandler(s.blob, interceptors)
	mux.Handle(blobPath, blobHandler)
	mux.HandleFunc("/sync", s.handleSync)
	mux.HandleFunc("/env", s.handleEnv)
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

	ctx := r.Context()
	var toResolve, buckets []resourceregistry.Entry
	for _, e := range s.registry.Snapshot() {
		if e.Type == linksv1.LinkType_LINK_TYPE_BUCKET {
			buckets = append(buckets, e)
		} else {
			toResolve = append(toResolve, e)
		}
	}

	cfg, err := s.account(ctx)
	if err != nil {
		err = fmt.Errorf("fetch project config: %w", err)
		s.deliverSync(SyncResult{Err: err})
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	resolved, err := s.resolve(ctx, cfg, toResolve)
	if err != nil {
		err = fmt.Errorf("resolve resources: %w", err)
		s.deliverSync(SyncResult{Err: err})
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	resolved = append(resolved, s.bucketResources(buckets)...)

	var liveValues map[string]string
	liveKeys := s.live.sorted()
	if len(liveKeys) > 0 {
		liveValues, err = s.fetchLiveValues(ctx, s.apiURL, s.token, s.projectID, liveKeys)
		if err != nil {
			err = fmt.Errorf("resolve live values (%s): %w", strings.Join(liveKeys, ", "), err)
			s.deliverSync(SyncResult{Err: err})
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}

	s.deliverSync(SyncResult{Account: cfg, Resources: resolved, DevServerAddress: s.devServerAddr, LiveValues: liveValues, LiveKeys: liveKeys})
	w.WriteHeader(http.StatusOK)
}

func (s *Server) bucketResources(buckets []resourceregistry.Entry) []resolve.Resource {
	out := make([]resolve.Resource, 0, len(buckets))
	for _, b := range buckets {
		value, _ := protojson.Marshal(&linksv1.Link{
			Name:       b.Name,
			Properties: &linksv1.Link_Bucket{Bucket: &linksv1.BucketProperties{Bucket: b.Name}},
		})
		out = append(out, resolve.Resource{
			Name: b.Name,
			Type: b.Type,
			Env:  map[string]string{naming.ResourceEnvName(b.Type, b.Name): string(value)},
		})
	}
	return out
}

func (s *Server) Discover(ctx context.Context, cfg *projectconfig.Config, stdout, stderr io.Writer) error {
	files, err := discovery.Discover(cfg.Dir, cfg.Discovery.Paths)
	if err != nil {
		return fmt.Errorf("discover resources: %w", err)
	}

	entry, err := discovery.Bundle(cfg.Dir, files)
	if err != nil {
		return fmt.Errorf("bundle discovery entrypoint: %w", err)
	}

	return discovery.Run(ctx, entry, s.devServerAddr, stdout, stderr)
}

func (s *Server) ClientKeys() ([]clientenv.Key, error) {
	_, gate := s.env.current()
	if gate == nil {
		return nil, nil
	}
	return clientenv.Declared(gate.Definitions())
}
