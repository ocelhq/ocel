// Package devserver runs the local Connect server that resource
// declarations register with during discovery, and handles the plain HTTP
// /sync route that triggers provisioning once discovery completes.
package devserver

import (
	"context"
	"fmt"
	"net/http"
	"sync"

	connect "connectrpc.com/connect"

	"github.com/ocelhq/ocel/internal/manifest"
	"github.com/ocelhq/ocel/internal/provision"
	devv1 "github.com/ocelhq/ocel/pkg/proto/dev/v1"
	"github.com/ocelhq/ocel/pkg/proto/dev/v1/devv1connect"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/resources/v1"
	"github.com/ocelhq/ocel/pkg/proto/resources/v1/resourcesv1connect"
)

// SyncResult is delivered on Server.Sync() once a /sync request has been
// handled, successfully or not.
type SyncResult struct {
	ProjectConfig provision.ProjectConfig
	Resources     []provision.ProvisionedResource
	Err           error
}

// Server accumulates declared resources via the Connect ResourceService and,
// on /sync, fetches project identity and provisions the declared manifest.
// It also serves DevService.Subscribe, pushing the full resolved env to
// followers as it's known (see PushEnv).
type Server struct {
	manifest  *manifest.Manifest
	apiURL    string
	token     string
	projectID string
	syncCh    chan SyncResult

	fetchProjectConfig func(ctx context.Context, apiURL, token, projectID string) (provision.ProjectConfig, error)
	provision          func(ctx context.Context, cfg provision.ProjectConfig, resources []manifest.Entry) ([]provision.ProvisionedResource, error)

	subMu       sync.Mutex
	latestEnv   *devv1.EnvUpdate
	subscribers map[chan *devv1.EnvUpdate]struct{}
}

// Option configures a Server constructed via New.
type Option func(*Server)

// WithProvisioner overrides the default stub fetchProjectConfig/provision
// implementations, e.g. to route provisioning through a local harness
// instead of the real Ocel API.
func WithProvisioner(
	fetchProjectConfig func(ctx context.Context, apiURL, token, projectID string) (provision.ProjectConfig, error),
	provisionFn func(ctx context.Context, cfg provision.ProjectConfig, resources []manifest.Entry) ([]provision.ProvisionedResource, error),
) Option {
	return func(s *Server) {
		s.fetchProjectConfig = fetchProjectConfig
		s.provision = provisionFn
	}
}

// New builds a Server that will authenticate provisioning calls with token
// against apiURL for projectID.
func New(apiURL, token, projectID string, opts ...Option) *Server {
	s := &Server{
		manifest:           manifest.New(),
		apiURL:             apiURL,
		token:              token,
		projectID:          projectID,
		syncCh:             make(chan SyncResult, 1),
		fetchProjectConfig: provision.FetchProjectConfig,
		provision:          provision.Provision,
		subscribers:        make(map[chan *devv1.EnvUpdate]struct{}),
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Declare implements resourcesv1connect.ResourceServiceHandler, recording
// the declared resource into the manifest.
func (s *Server) Declare(_ context.Context, req *resourcesv1.DeclareRequest) (*resourcesv1.DeclareResponse, error) {
	if req.Resource.Type == resourcesv1.ResourceType_RESOURCE_TYPE_UNSPECIFIED {
		return nil, fmt.Errorf("unsupported resource type: %v", req.Resource.Type)
	}

	s.manifest.Add(manifest.Entry{Name: req.Resource.Name, Type: req.Resource.Type})
	return &resourcesv1.DeclareResponse{}, nil
}

// Mux returns the HTTP handler serving the Connect ResourceService, the
// Connect DevService, and the plain /sync route.
func (s *Server) Mux() *http.ServeMux {
	mux := http.NewServeMux()
	resourcePath, resourceHandler := resourcesv1connect.NewResourceServiceHandler(s)
	mux.Handle(resourcePath, resourceHandler)
	devPath, devHandler := devv1connect.NewDevServiceHandler(s)
	mux.Handle(devPath, devHandler)
	mux.HandleFunc("/sync", s.handleSync)
	return mux
}

// PushEnv records env as the latest full resolved environment and delivers
// it to every connected follower. It's also handed to any follower that
// subscribes afterwards (see Subscribe), so followers always see the
// current state regardless of connection order.
func (s *Server) PushEnv(env map[string]string) {
	update := &devv1.EnvUpdate{Env: env}

	s.subMu.Lock()
	defer s.subMu.Unlock()
	s.latestEnv = update
	for ch := range s.subscribers {
		select {
		case ch <- update:
		default:
			// Slow subscriber: it'll get this state (or a newer one) on its
			// next receive since ch already holds an undelivered update.
		}
	}
}

// Subscribe implements devv1connect.DevServiceHandler, streaming the latest
// resolved env to the caller immediately (if one is already known) and every
// time PushEnv is called thereafter, until ctx is done.
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

// Sync returns the channel a single SyncResult is delivered on once /sync
// has been handled.
func (s *Server) Sync() <-chan SyncResult {
	return s.syncCh
}

func (s *Server) handleSync(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx := r.Context()
	resources := s.manifest.Snapshot()

	cfg, err := s.fetchProjectConfig(ctx, s.apiURL, s.token, s.projectID)
	if err != nil {
		err = fmt.Errorf("fetch project config: %w", err)
		s.syncCh <- SyncResult{Err: err}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	provisioned, err := s.provision(ctx, cfg, resources)
	if err != nil {
		err = fmt.Errorf("provision resources: %w", err)
		s.syncCh <- SyncResult{Err: err}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	s.syncCh <- SyncResult{ProjectConfig: cfg, Resources: provisioned}
	w.WriteHeader(http.StatusOK)
}
