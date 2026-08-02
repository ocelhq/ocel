// Package devserver runs the local Connect server that resource
// declarations register with during discovery, and handles the plain HTTP
// /sync route that triggers provisioning once discovery completes.
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

// SyncResult is delivered on Server.Sync() once a /sync request has been
// handled, successfully or not.
type SyncResult struct {
	ProjectConfig provision.ProjectConfig
	Resources     []provision.ProvisionedResource
	// LiveValues holds the plaintext of every live-class key this run
	// declared, resolved once here because dev has nothing to resolve one
	// later (see DeclareEnv).
	LiveValues map[string]string
	// LiveKeys names those keys. It is what the run declared rather than what
	// the source gave back, so it is the honest subject of anything said about
	// dev's live-value semantics: a source that resolved none of them is still
	// a run whose live values were resolved once, at startup, and will not be
	// resolved again.
	LiveKeys []string
	Err      error
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
	detector      *detector
	syncCh        chan SyncResult

	fetchProjectConfig func(ctx context.Context, apiURL, token, projectID string) (provision.ProjectConfig, error)
	provision          func(ctx context.Context, cfg provision.ProjectConfig, resources []manifest.Entry) ([]provision.ProvisionedResource, error)
	fetchLiveValues    func(ctx context.Context, apiURL, token, projectID string, keys []string) (map[string]string, error)

	// cfgMu guards the project config the run resolved before discovery, when
	// one was installed. Nil for the blob rig, which resolves none.
	cfgMu      sync.Mutex
	projectCfg *provision.ProjectConfig

	liveMu   sync.Mutex
	liveKeys map[string]struct{}

	// declareMu keeps one declaration's record-then-answer whole against the
	// concurrent ones the SDK issues.
	declareMu sync.Mutex

	// varsMu guards the pair below, which is replaced wholesale on every
	// re-discovery. Both are nil until UseValues installs them: the blob rig
	// serves declarations without gating them.
	varsMu sync.Mutex
	values map[string]string
	scope  envgate.Scope
	store  *flatValues
	gate   *envgate.Gate

	subMu       sync.Mutex
	latestEnv   *devv1.EnvUpdate
	subscribers map[chan *devv1.EnvUpdate]struct{}
}

// New builds a Server that will authenticate provisioning calls with token
// against apiURL for projectID. devServerAddr is this dev server's own base
// URL (e.g. http://127.0.0.1:PORT): it's the injected address every declared
// bucket's OCEL_RESOURCE_BUCKET_<name> resolves to, and the address the dev
// BucketService is reached at.
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

// RunDetector runs the client-independent completion loop until ctx is done:
// each tick asks the Ocel API to detect this project's landed objects (which
// performs the atomic pending -> succeeded transition) and delivers each
// newly-succeeded file to its app route as op=callback. It blocks, so callers
// run it in its own goroutine tied to the dev server's lifetime. Sweep errors
// are reported via reportErr and never stop the loop.
func (s *Server) RunDetector(ctx context.Context, reportErr func(error)) {
	s.detector.run(ctx, reportErr)
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

// UseValues gives dev a variable store — one flat map of root values, keyed by
// nothing but the key name — and the scope a refusal has to name. Installing it
// turns on the same gate a deploy runs: declarations are answered from these
// values, the declaring process's schema verdict is kept, and CheckEnv refuses
// a run no app could resolve.
//
// It is a separate call rather than a New parameter because the store is dev's
// alone. `ocel run` and the blob rig serve the same declarations with no store
// behind them and must keep answering with no cells.
func (s *Server) UseValues(values map[string]string, scope envgate.Scope) {
	s.varsMu.Lock()
	defer s.varsMu.Unlock()
	s.values = values
	s.scope = scope
	s.store = newFlatValues(values)
	s.gate = envgate.New(s.store, scope)
}

// UseProjectConfig installs the project config the caller already resolved, so
// /sync provisions against the same one the gate ruled from rather than one the
// verdict never saw.
func (s *Server) UseProjectConfig(cfg provision.ProjectConfig) {
	s.cfgMu.Lock()
	defer s.cfgMu.Unlock()
	s.projectCfg = &cfg
}

// projectConfig is the installed config, or the server's own fetch when no
// caller installed one.
func (s *Server) projectConfig(ctx context.Context) (provision.ProjectConfig, error) {
	s.cfgMu.Lock()
	cfg := s.projectCfg
	s.cfgMu.Unlock()
	if cfg != nil {
		return *cfg, nil
	}
	return s.fetchProjectConfig(ctx, s.apiURL, s.token, s.projectID)
}

// variables returns the installed store and gate together, since a caller that
// has one always needs the other.
func (s *Server) variables() (*flatValues, *envgate.Gate) {
	s.varsMu.Lock()
	defer s.varsMu.Unlock()
	return s.store, s.gate
}

// DeclareEnv and ReportEnvProblems implement the declaration half of
// resourcesv1connect.ResourceServiceHandler. With no store installed they are
// the pre-variables dev server: no cells, no verdict, so a dev run is never
// blocked by a value only a deploy needs.
//
// With a store installed the gate answers instead, exactly as it does for a
// deploy. Only the store beneath it differs, and the store is re-read before
// each answer because the folders a key is scoped to arrive with the
// declaration itself — a flat file has none of its own.
//
// A live-class key is the one thing the gate cannot deliver, in dev as in a
// deploy: it is answered as presence with no plaintext. Dev records it here and
// resolves it at sync (see handleSync), eagerly and once, then hands it to the
// child in the environment — the timing differs from a deploy's, the call site
// does not.
func (s *Server) DeclareEnv(ctx context.Context, req *resourcesv1.DeclareEnvRequest) (*resourcesv1.DeclareEnvResponse, error) {
	s.liveMu.Lock()
	for _, d := range req.GetDefinitions() {
		if d.GetClass() == resourcesv1.VariableClass_VARIABLE_CLASS_SECRET {
			s.liveKeys[d.GetKey()] = struct{}{}
		}
	}
	s.liveMu.Unlock()

	// The SDK issues one of these per defineEnv call and they are concurrent,
	// so the three steps are one step: a declaration that recorded its folders
	// and then read a cell set another declaration installed in between would
	// be answered without the cells it just asked for.
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

// CheckEnv is the gate's verdict for the declarations this run made: nil when
// every app can resolve every required cell, a *envgate.Refusal otherwise. A
// server with no store installed gates nothing.
//
// It re-reads the store first. A key's folders are learned from the declaration
// that names them, so the cells the store holds are only complete once every
// declaration has landed — which is here, and not at any one of them.
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

// ScopedFolders is every key this run declared with a folder scope, mapped to
// the folders it is scoped to. It answers the one question the gate cannot: the
// gate rules per app, over each app's own binding, and dev states one binding
// for the whole project — so deciding whether that binding costs a read needs
// the folders, not only the names.
//
// Two declarations of one key contribute the union of their folders, the way
// the store accumulates their cells: a key readable in either folder is scoped
// to both.
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

// declaredLiveKeys is every live-class key declared since the last reset,
// sorted so the same declarations always produce the same request.
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

// ResetManifest clears every declared resource and variable, so the next full
// re-discovery's declares fully replace (rather than accumulate onto) the
// prior set before the following /sync provisions them. The gate is rebuilt
// with it: a verdict is about one discovery run, so a refusal the edit just
// fixed must not outlive the run that earned it.
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

// Mux returns the HTTP handler serving the Connect ResourceService, the
// Connect DevService, and the plain /sync route.
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

	// A run declaring no live key never asks: dev mirrors the deployed
	// guarantee that only the functions declaring live values are exposed to
	// their source being down.
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

// bucketResources synthesizes the OCEL_RESOURCE_BUCKET_<name> env for each
// declared bucket: JSON {address, bucket} where address is this dev server
// (whose BucketService the SDK dials) and bucket is the declared logical
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

// Discover bundles cfg's declaration files and runs them in a node process
// pointed at this server, which is the only address they can be pointed at:
// the server holds it, so the run and the handler answering it cannot drift
// apart. It is the dev half of the discovery pass a deploy makes through
// deploycollector, and both go through discovery.Run.
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

// ClientKeys is every client-accessible key this run declared, sorted so the
// same declarations always produce the same accessor. They are the browser's
// half of dev's environment: the generated accessor names them, and the child
// is spawned with each one exported a second time under the framework's public
// prefix so the framework's static replacement has something to inline.
//
// The plaintext class is the only one that can carry client access — combining
// it with an encrypted class is a definition error — so a key that somehow
// arrived otherwise is not one a browser may be handed.
func (s *Server) ClientKeys() []string {
	_, gate := s.variables()
	if gate == nil {
		return nil
	}
	var keys []string
	for _, definition := range gate.Definitions() {
		if definition.GetClientAccessible() && definition.GetClass() == resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN {
			keys = append(keys, definition.GetKey())
		}
	}
	sort.Strings(keys)
	return keys
}
