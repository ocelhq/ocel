// Package devserver runs the local Connect server that resource
// declarations register with during discovery, and handles the plain HTTP
// /sync route that triggers provisioning once discovery completes.
package devserver

import (
	"context"
	"fmt"
	"net/http"

	"github.com/ocelhq/ocel/internal/manifest"
	"github.com/ocelhq/ocel/internal/provision"
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
type Server struct {
	manifest  *manifest.Manifest
	apiURL    string
	token     string
	projectID string
	syncCh    chan SyncResult

	fetchProjectConfig func(ctx context.Context, apiURL, token, projectID string) (provision.ProjectConfig, error)
	provision          func(ctx context.Context, cfg provision.ProjectConfig, resources []manifest.Entry) ([]provision.ProvisionedResource, error)
}

// New builds a Server that will authenticate provisioning calls with token
// against apiURL for projectID.
func New(apiURL, token, projectID string) *Server {
	return &Server{
		manifest:           manifest.New(),
		apiURL:             apiURL,
		token:              token,
		projectID:          projectID,
		syncCh:             make(chan SyncResult, 1),
		fetchProjectConfig: provision.FetchProjectConfig,
		provision:          provision.Provision,
	}
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

// Mux returns the HTTP handler serving both the Connect ResourceService and
// the plain /sync route.
func (s *Server) Mux() *http.ServeMux {
	mux := http.NewServeMux()
	path, handler := resourcesv1connect.NewResourceServiceHandler(s)
	mux.Handle(path, handler)
	mux.HandleFunc("/sync", s.handleSync)
	return mux
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
