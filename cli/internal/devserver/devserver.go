package devserver

import (
	"context"
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
	"github.com/ocelhq/ocel/cli/internal/manifest"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/internal/provision"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/proto/buckets/v1/bucketsv1connect"
	devv1 "github.com/ocelhq/ocel/pkg/proto/dev/v1"
	"github.com/ocelhq/ocel/pkg/proto/dev/v1/devv1connect"
	linksv1 "github.com/ocelhq/ocel/pkg/proto/links/v1"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/resources/v1"
	"github.com/ocelhq/ocel/pkg/proto/resources/v1/resourcesv1connect"
	"google.golang.org/protobuf/encoding/protojson"
)

type SyncResult struct {
	ProjectConfig  provision.ProjectConfig
	Resources      []provision.Resource
	RuntimeAddress string
	LiveValues     map[string]string
	LiveKeys       []string
	Err            error
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
	provision          func(ctx context.Context, cfg provision.ProjectConfig, resources []manifest.Entry) ([]provision.Resource, error)
	fetchLiveValues    func(ctx context.Context, apiURL, token, projectID string, keys []string) (map[string]string, error)

	config *configCache
	live   *liveKeys
	env    *envState
	fanout *envFanout
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
		provision:          provision.Run,
		fetchLiveValues:    provision.FetchLiveValues,
		config:             newConfigCache(),
		live:               newLiveKeys(),
		env:                newEnvState(),
		fanout:             newEnvFanout(),
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
	s.env.use(values, scope)
}

func (s *Server) UseProjectConfig(cfg provision.ProjectConfig) {
	s.config.use(cfg)
}

func (s *Server) projectConfig(ctx context.Context) (provision.ProjectConfig, error) {
	if cfg, ok := s.config.held(); ok {
		return cfg, nil
	}
	return s.fetchProjectConfig(ctx, s.apiURL, s.token, s.projectID)
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
	s.manifest.Reset()
	s.live.reset()
	s.env.forgetDeclarations()
}

func (s *Server) Mux() *http.ServeMux {
	mux := http.NewServeMux()
	interceptors := connect.WithInterceptors(validate.NewInterceptor())
	resourcePath, resourceHandler := resourcesv1connect.NewResourceServiceHandler(s, interceptors)
	mux.Handle(resourcePath, resourceHandler)
	devPath, devHandler := devv1connect.NewDevServiceHandler(s, interceptors)
	mux.Handle(devPath, devHandler)
	runtimePath, runtimeHandler := bucketsv1connect.NewBucketServiceHandler(s.runtime, interceptors)
	mux.Handle(runtimePath, runtimeHandler)
	mux.HandleFunc("/sync", s.handleSync)
	return mux
}

func (s *Server) PushEnv(env map[string]string) {
	s.fanout.push(&devv1.EnvUpdate{Env: env})
}

func (s *Server) Subscribe(ctx context.Context, _ *devv1.SubscribeRequest, stream *connect.ServerStream[devv1.EnvUpdate]) error {
	ch := s.fanout.subscribe()
	defer s.fanout.unsubscribe(ch)

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
		if e.Type == linksv1.LinkType_LINK_TYPE_BUCKET {
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

	s.deliverSync(SyncResult{ProjectConfig: cfg, Resources: provisioned, RuntimeAddress: s.devServerAddr, LiveValues: liveValues, LiveKeys: liveKeys})
	w.WriteHeader(http.StatusOK)
}

func (s *Server) bucketResources(buckets []manifest.Entry) []provision.Resource {
	out := make([]provision.Resource, 0, len(buckets))
	for _, b := range buckets {
		value, _ := protojson.Marshal(&linksv1.Link{
			Name:       b.Name,
			Properties: &linksv1.Link_Bucket{Bucket: &linksv1.BucketProperties{Bucket: b.Name}},
		})
		out = append(out, provision.Resource{
			Name: b.Name,
			Type: b.Type,
			Env:  map[string]string{"OCEL_RESOURCE_" + naming.EnvFragment(b.Type) + "_" + b.Name: string(value)},
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
	_, gate := s.env.current()
	if gate == nil {
		return nil
	}
	return clientenv.DeclaredKeys(gate.Definitions())
}
