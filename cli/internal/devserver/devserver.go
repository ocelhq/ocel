// Package devserver runs the local Connect server that resource
// declarations register with during discovery, and handles the plain HTTP
// /sync route that triggers provisioning once discovery completes.
package devserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"

	connect "connectrpc.com/connect"

	"github.com/ocelhq/ocel/cli/internal/declare"
	"github.com/ocelhq/ocel/cli/internal/manifest"
	"github.com/ocelhq/ocel/cli/internal/provision"
	devv1 "github.com/ocelhq/ocel/pkg/proto/dev/v1"
	"github.com/ocelhq/ocel/pkg/proto/dev/v1/devv1connect"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/resources/v1"
	"github.com/ocelhq/ocel/pkg/proto/resources/v1/resourcesv1connect"
	"github.com/ocelhq/ocel/pkg/proto/runtime/v1/runtimev1connect"
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
	manifest      *manifest.Manifest
	apiURL        string
	token         string
	projectID     string
	devServerAddr string
	runtime       *runtimeShim
	syncCh        chan SyncResult

	fetchProjectConfig func(ctx context.Context, apiURL, token, projectID string) (provision.ProjectConfig, error)
	provision          func(ctx context.Context, cfg provision.ProjectConfig, resources []manifest.Entry) ([]provision.ProvisionedResource, error)

	subMu       sync.Mutex
	latestEnv   *devv1.EnvUpdate
	subscribers map[chan *devv1.EnvUpdate]struct{}
}

// New builds a Server that will authenticate provisioning calls with token
// against apiURL for projectID. devServerAddr is this dev server's own base
// URL (e.g. http://127.0.0.1:PORT): it's the injected address every declared
// bucket's OCEL_RESOURCE_BUCKET_<name> resolves to, and the address the dev
// RuntimeService is reached at.
func New(apiURL, token, projectID, devServerAddr string) *Server {
	return &Server{
		manifest:           manifest.New(),
		apiURL:             apiURL,
		token:              token,
		projectID:          projectID,
		devServerAddr:      devServerAddr,
		runtime:            newRuntimeShim(apiURL, token, projectID),
		syncCh:             make(chan SyncResult, 1),
		fetchProjectConfig: provision.FetchProjectConfig,
		provision:          provision.Provision,
		subscribers:        make(map[chan *devv1.EnvUpdate]struct{}),
	}
}

// Declare implements resourcesv1connect.ResourceServiceHandler, recording
// the declared resource into the manifest.
func (s *Server) Declare(_ context.Context, req *resourcesv1.DeclareRequest) (*resourcesv1.DeclareResponse, error) {
	res, err := declare.Parse(req)
	if err != nil {
		return nil, err
	}

	s.manifest.Add(manifest.Entry{Name: res.Name, Type: res.Type})
	return &resourcesv1.DeclareResponse{}, nil
}

// ResetManifest clears every declared resource, so the next full
// re-discovery's declares fully replace (rather than accumulate onto) the
// prior set before the following /sync provisions them.
func (s *Server) ResetManifest() {
	s.manifest.Reset()
}

// Mux returns the HTTP handler serving the Connect ResourceService, the
// Connect DevService, and the plain /sync route.
func (s *Server) Mux() *http.ServeMux {
	mux := http.NewServeMux()
	resourcePath, resourceHandler := resourcesv1connect.NewResourceServiceHandler(s)
	mux.Handle(resourcePath, resourceHandler)
	devPath, devHandler := devv1connect.NewDevServiceHandler(s)
	mux.Handle(devPath, devHandler)
	runtimePath, runtimeHandler := runtimev1connect.NewRuntimeServiceHandler(s.runtime)
	mux.Handle(runtimePath, runtimeHandler)
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

// Sync returns the channel the latest SyncResult is delivered on once /sync
// has been handled. A result left unconsumed (e.g. discovery failed after
// /sync was already handled) is replaced by the next sync's result.
func (s *Server) Sync() <-chan SyncResult {
	return s.syncCh
}

// deliverSync publishes res as the latest sync result, evicting an
// unconsumed prior result so the send never blocks the /sync handler.
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
	// Buckets are runtime-served, not resolve-provisioned: they need no
	// per-user provisioning (dev shares one bucket, isolated by prefix at
	// presign), so they're filtered out of the resolve request and their env
	// is synthesized locally. This also keeps the resolve endpoint - which
	// 400s on unknown resource types - from ever seeing a BUCKET.
	var toResolve, buckets []manifest.Entry
	for _, e := range s.manifest.Snapshot() {
		if e.Type == resourcesv1.ResourceType_RESOURCE_TYPE_BUCKET {
			buckets = append(buckets, e)
		} else {
			toResolve = append(toResolve, e)
		}
	}

	cfg, err := s.fetchProjectConfig(ctx, s.apiURL, s.token, s.projectID)
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

	s.deliverSync(SyncResult{ProjectConfig: cfg, Resources: provisioned})
	w.WriteHeader(http.StatusOK)
}

// bucketResources synthesizes the OCEL_RESOURCE_BUCKET_<name> env for each
// declared bucket: JSON {address, bucket} where address is this dev server
// (whose RuntimeService the SDK dials) and bucket is the declared logical
// name. No cloud round-trip - the presign endpoint owns the store mechanics.
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
