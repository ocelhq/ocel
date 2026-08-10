package devserver

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"sort"
	"strings"
	"sync"

	connect "connectrpc.com/connect"

	"github.com/ocelhq/ocel/cli/internal/clientenv"
	"github.com/ocelhq/ocel/cli/internal/declare"
	"github.com/ocelhq/ocel/cli/internal/discovery"
	"github.com/ocelhq/ocel/cli/internal/envgate"
	"github.com/ocelhq/ocel/cli/internal/manifest"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/internal/provision"
	"github.com/ocelhq/ocel/pkg/proto/buckets/v1/bucketsv1connect"
	devv1 "github.com/ocelhq/ocel/pkg/proto/dev/v1"
	"github.com/ocelhq/ocel/pkg/proto/dev/v1/devv1connect"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/resources/v1"
	"github.com/ocelhq/ocel/pkg/proto/resources/v1/resourcesv1connect"
)

type SyncResult struct {
	ProjectConfig provision.ProjectConfig
	Resources     []provision.ProvisionedResource
	LiveValues    map[string]string
	LiveKeys      []string
	Err           error
}

type Server struct {
	manifest      *manifest.Manifest
	apiURL        string
	token         string
	projectID     string
	devServerAddr string
	runtime       *runtimeShim
	detector      *detector
	syncCh        chan SyncResult

	fetchProjectConfig func(ctx context.Context, apiURL, token, projectID string) (provision.ProjectConfig, error)
	provision          func(ctx context.Context, cfg provision.ProjectConfig, resources []manifest.Entry) ([]provision.ProvisionedResource, error)
	fetchLiveValues    func(ctx context.Context, apiURL, token, projectID string, keys []string) (map[string]string, error)

	cfgMu      sync.Mutex
	projectCfg *provision.ProjectConfig

	liveMu   sync.Mutex
	liveKeys map[string]struct{}

	declareMu sync.Mutex

	varsMu sync.Mutex
	values map[string]string
	scope  envgate.Scope
	store  *flatValues
	gate   *envgate.Gate

	subMu       sync.Mutex
	latestEnv   *devv1.EnvUpdate
	subscribers map[chan *devv1.EnvUpdate]struct{}
}

func New(apiURL, token, projectID, devServerAddr string) *Server {
	return &Server{
		manifest:           manifest.New(),
		apiURL:             apiURL,
		token:              token,
		projectID:          projectID,
		devServerAddr:      devServerAddr,
		runtime:            newRuntimeShim(apiURL, token, projectID),
		detector:           newDetector(apiURL, token, projectID),
		syncCh:             make(chan SyncResult, 1),
		fetchProjectConfig: provision.FetchProjectConfig,
		provision:          provision.Provision,
		fetchLiveValues:    provision.FetchLiveValues,
		liveKeys:           make(map[string]struct{}),
		subscribers:        make(map[chan *devv1.EnvUpdate]struct{}),
	}
}

func (s *Server) RunDetector(ctx context.Context, reportErr func(error)) {
	s.detector.run(ctx, reportErr)
}

func (s *Server) Declare(_ context.Context, req *resourcesv1.DeclareRequest) (*resourcesv1.DeclareResponse, error) {
	res, err := declare.Parse(req)
	if err != nil {
		return nil, err
	}

	s.manifest.Add(manifest.Entry{Name: res.Name, Type: res.Type})
	return &resourcesv1.DeclareResponse{}, nil
}

func (s *Server) UseValues(values map[string]string, scope envgate.Scope) {
	s.varsMu.Lock()
	defer s.varsMu.Unlock()
	s.values = values
	s.scope = scope
	s.store = newFlatValues(values)
	s.gate = envgate.New(s.store, scope)
}

func (s *Server) UseProjectConfig(cfg provision.ProjectConfig) {
	s.cfgMu.Lock()
	defer s.cfgMu.Unlock()
	s.projectCfg = &cfg
}

func (s *Server) projectConfig(ctx context.Context) (provision.ProjectConfig, error) {
	s.cfgMu.Lock()
	cfg := s.projectCfg
	s.cfgMu.Unlock()
	if cfg != nil {
		return *cfg, nil
	}
	return s.fetchProjectConfig(ctx, s.apiURL, s.token, s.projectID)
}

func (s *Server) variables() (*flatValues, *envgate.Gate) {
	s.varsMu.Lock()
	defer s.varsMu.Unlock()
	return s.store, s.gate
}

func (s *Server) DeclareEnv(ctx context.Context, req *resourcesv1.DeclareEnvRequest) (*resourcesv1.DeclareEnvResponse, error) {
	s.liveMu.Lock()
	for _, d := range req.GetDefinitions() {
		if d.GetClass() == resourcesv1.VariableClass_VARIABLE_CLASS_SECRET {
			s.liveKeys[d.GetKey()] = struct{}{}
		}
	}
	s.liveMu.Unlock()

	s.declareMu.Lock()
	defer s.declareMu.Unlock()

	store, gate := s.variables()
	if gate == nil {
		return &resourcesv1.DeclareEnvResponse{}, nil
	}
	store.Declare(req.GetDefinitions())
	if err := gate.Prefetch(ctx); err != nil {
		return nil, err
	}
	return gate.DeclareEnv(ctx, req)
}

func (s *Server) CheckEnv(ctx context.Context) error {
	_, gate := s.variables()
	if gate == nil {
		return nil
	}
	if err := gate.Prefetch(ctx); err != nil {
		return err
	}
	return gate.Check()
}

func (s *Server) ScopedFolders() map[string][]string {
	_, gate := s.variables()
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
		sort.Strings(scoped[key])
	}
	return scoped
}

func (s *Server) declaredLiveKeys() []string {
	s.liveMu.Lock()
	defer s.liveMu.Unlock()
	keys := make([]string, 0, len(s.liveKeys))
	for key := range s.liveKeys {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func (s *Server) ReportEnvProblems(ctx context.Context, req *resourcesv1.ReportEnvProblemsRequest) (*resourcesv1.ReportEnvProblemsResponse, error) {
	if _, gate := s.variables(); gate != nil {
		return gate.ReportEnvProblems(ctx, req)
	}
	return &resourcesv1.ReportEnvProblemsResponse{}, nil
}

func (s *Server) ResetManifest() {
	s.manifest.Reset()
	s.liveMu.Lock()
	s.liveKeys = make(map[string]struct{})
	s.liveMu.Unlock()

	s.varsMu.Lock()
	defer s.varsMu.Unlock()
	if s.gate != nil {
		s.store = newFlatValues(s.values)
		s.gate = envgate.New(s.store, s.scope)
	}
}

func (s *Server) Mux() *http.ServeMux {
	mux := http.NewServeMux()
	resourcePath, resourceHandler := resourcesv1connect.NewResourceServiceHandler(s)
	mux.Handle(resourcePath, resourceHandler)
	devPath, devHandler := devv1connect.NewDevServiceHandler(s)
	mux.Handle(devPath, devHandler)
	runtimePath, runtimeHandler := bucketsv1connect.NewBucketServiceHandler(s.runtime)
	mux.Handle(runtimePath, runtimeHandler)
	mux.HandleFunc("/sync", s.handleSync)
	return mux
}

func (s *Server) PushEnv(env map[string]string) {
	update := &devv1.EnvUpdate{Env: env}

	s.subMu.Lock()
	defer s.subMu.Unlock()
	s.latestEnv = update
	for ch := range s.subscribers {
		select {
		case ch <- update:
		default:
		}
	}
}

func (s *Server) Subscribe(ctx context.Context, _ *devv1.SubscribeRequest, stream *connect.ServerStream[devv1.EnvUpdate]) error {
	ch := make(chan *devv1.EnvUpdate, 1)

	s.subMu.Lock()
	if s.latestEnv != nil {
		ch <- s.latestEnv
	}
	s.subscribers[ch] = struct{}{}
	s.subMu.Unlock()

	defer func() {
		s.subMu.Lock()
		delete(s.subscribers, ch)
		s.subMu.Unlock()
	}()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case update := <-ch:
			if err := stream.Send(update); err != nil {
				return err
			}
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
	var toResolve, buckets []manifest.Entry
	for _, e := range s.manifest.Snapshot() {
		if e.Type == resourcesv1.ResourceType_RESOURCE_TYPE_BUCKET {
			buckets = append(buckets, e)
		} else {
			toResolve = append(toResolve, e)
		}
	}

	cfg, err := s.projectConfig(ctx)
	if err != nil {
		err = fmt.Errorf("fetch project config: %w", err)
		s.deliverSync(SyncResult{Err: err})
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	provisioned, err := s.provision(ctx, cfg, toResolve)
	if err != nil {
		err = fmt.Errorf("provision resources: %w", err)
		s.deliverSync(SyncResult{Err: err})
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	provisioned = append(provisioned, s.bucketResources(buckets)...)

	var liveValues map[string]string
	liveKeys := s.declaredLiveKeys()
	if len(liveKeys) > 0 {
		liveValues, err = s.fetchLiveValues(ctx, s.apiURL, s.token, s.projectID, liveKeys)
		if err != nil {
			err = fmt.Errorf("resolve live values (%s): %w", strings.Join(liveKeys, ", "), err)
			s.deliverSync(SyncResult{Err: err})
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}

	s.deliverSync(SyncResult{ProjectConfig: cfg, Resources: provisioned, LiveValues: liveValues, LiveKeys: liveKeys})
	w.WriteHeader(http.StatusOK)
}

func (s *Server) bucketResources(buckets []manifest.Entry) []provision.ProvisionedResource {
	out := make([]provision.ProvisionedResource, 0, len(buckets))
	for _, b := range buckets {
		value, _ := json.Marshal(map[string]string{
			"address": s.devServerAddr,
			"bucket":  b.Name,
		})
		out = append(out, provision.ProvisionedResource{
			Name: b.Name,
			Type: b.Type,
			Env:  map[string]string{"OCEL_RESOURCE_BUCKET_" + b.Name: string(value)},
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

func (s *Server) ClientKeys() []string {
	_, gate := s.variables()
	if gate == nil {
		return nil
	}
	return clientenv.DeclaredKeys(gate.Definitions())
}
