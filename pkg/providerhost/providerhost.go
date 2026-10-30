package providerhost

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"

	"github.com/ocelhq/ocel/pkg/endpoint"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	"github.com/ocelhq/ocel/pkg/proto/provider/cost/v1/costv1connect"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/providerserver"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/seal"
)

const (
	HeaderSession = "Ocel-Session"
	HeaderVersion = "Ocel-Version"
	HeaderSlug    = "Ocel-Slug"
	HeaderTier    = "Ocel-Tier"
)

const (
	defaultIdleAfter          = 10 * time.Minute
	defaultMaxSessions        = 4096
	defaultMaxPerOrganization = 64
	defaultQuestionWait       = time.Minute
)

type Config struct {
	Version string

	Base func(ctx context.Context, caller endpoint.Caller) (provider.Settings, error)
	New  func(ctx context.Context, caller endpoint.Caller, session string, settings provider.Settings) (provider.Provider, error)

	Serves []string

	Named   *NamedSessions
	Scratch string

	MaxSessions int
	IdleAfter   time.Duration
	Now         func() time.Time

	questionWait time.Duration
}

type NamedSessions struct {
	Records            records.Store
	Cipher             seal.Cipher
	MaxPerOrganization int
}

type Host struct {
	config Config
	errors *connect.ErrorWriter
	serves map[string]bool

	mu       sync.Mutex
	sessions map[string]*session

	stop    context.CancelFunc
	stopped chan struct{}
}

type session struct {
	key    string
	id     string
	tenant string
	named  bool
	dir    string

	tier             environment.Tier
	records          records.Store
	cipher           seal.Cipher
	deployedRevision records.Revision

	mux   http.Handler
	inner *providerserver.Session
	ready chan struct{}
	err   error

	lastUsed   time.Time
	calls      int
	configured bool
	answering  map[string]bool
}

func New(config Config) (*Host, error) {
	if config.Base == nil || config.New == nil {
		return nil, errors.New("providerhost: Config.Base and Config.New are required")
	}
	if len(config.Serves) == 0 {
		return nil, errors.New("providerhost: Config.Serves names no procedure")
	}
	if config.Named != nil {
		if config.Named.Records == nil || config.Named.Cipher == nil || config.Version == "" {
			return nil, errors.New("providerhost: named sessions need Records, Cipher and the Version served")
		}
		if config.Named.MaxPerOrganization == 0 {
			config.Named.MaxPerOrganization = defaultMaxPerOrganization
		}
	}
	if config.Scratch == "" {
		config.Scratch = os.TempDir()
	}
	if config.IdleAfter == 0 {
		config.IdleAfter = defaultIdleAfter
	}
	if config.MaxSessions == 0 {
		config.MaxSessions = defaultMaxSessions
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	if config.questionWait == 0 {
		config.questionWait = defaultQuestionWait
	}
	if err := os.MkdirAll(config.Scratch, 0o700); err != nil {
		return nil, err
	}
	ctx, stop := context.WithCancel(context.Background())
	serves := make(map[string]bool, len(config.Serves))
	for _, procedure := range config.Serves {
		serves[procedure] = true
	}
	h := &Host{config: config, errors: connect.NewErrorWriter(), serves: serves, sessions: map[string]*session{}, stop: stop, stopped: make(chan struct{})}
	go h.relayAndSweep(ctx)
	return h, nil
}

func (h *Host) Mounts() []endpoint.Mount {
	return []endpoint.Mount{
		{Path: "/" + contractv1connect.ProviderServiceName + "/", Handler: h},
		{Path: "/" + costv1connect.CostServiceName + "/", Handler: h},
	}
}

func (h *Host) HoldsSession(caller endpoint.Caller, id string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	s, ok := h.sessions[namedKey(caller.Tenant(), id)]
	return ok && s.tenant == caller.Tenant() && !h.isIdle(s)
}

func (h *Host) Close() {
	h.stop()
	<-h.stopped
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, s := range h.sessions {
		h.end(s)
	}
}

func (h *Host) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	procedure := r.URL.Path
	if !strings.HasPrefix(procedure, "/"+contractv1connect.ProviderServiceName+"/") && !strings.HasPrefix(procedure, "/"+costv1connect.CostServiceName+"/") {
		h.fail(w, r, connect.CodeUnimplemented, "this endpoint hosts no %s", procedure)
		return
	}
	caller, ok := endpoint.CallerOf(r.Context())
	if !ok {
		h.fail(w, r, connect.CodeUnauthenticated, "no verified caller reached the provider host")
		return
	}
	if !h.serves[procedure] {
		_ = h.errors.Write(w, r, provider.RefusalError(provider.RefuseNeedsRunner(path.Base(procedure))))
		return
	}
	var s *session
	var err error
	if id := r.Header.Get(HeaderSession); id != "" {
		s, err = h.enterNamed(r, caller, id, procedure == contractv1connect.ProviderServiceConfigureProcedure)
	} else {
		s, err = h.enterSessionless(r, caller, procedure)
	}
	if err != nil {
		_ = h.errors.Write(w, r, err)
		return
	}
	defer h.leave(s)
	s.mux.ServeHTTP(w, r)
}

func (h *Host) fail(w http.ResponseWriter, r *http.Request, code connect.Code, format string, args ...any) {
	_ = h.errors.Write(w, r, connect.NewError(code, fmt.Errorf(format, args...)))
}

func (h *Host) enter(ctx context.Context, key string, open func() (*session, error), prepare func(context.Context, *session) error) (*session, error) {
	h.mu.Lock()
	h.sweep()
	s, held := h.sessions[key]
	if !held {
		var err error
		if s, err = open(); err != nil {
			h.mu.Unlock()
			return nil, err
		}
		h.sessions[key] = s
	}
	s.calls++
	s.lastUsed = h.config.Now()
	h.mu.Unlock()

	if !held {
		h.prepareSession(ctx, s, prepare)
	}
	select {
	case <-s.ready:
	case <-ctx.Done():
		h.leave(s)
		return nil, ctx.Err()
	}
	err := s.err
	if err == nil && s.named {
		err = h.touch(ctx, s)
	}
	if err != nil {
		h.leave(s)
		return nil, err
	}
	return s, nil
}

func (h *Host) prepareSession(ctx context.Context, s *session, prepare func(context.Context, *session) error) {
	defer close(s.ready)
	err := prepare(ctx, s)
	if err == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	s.err = err
	if current, ok := h.sessions[s.key]; ok && current == s {
		h.end(s)
	}
}

func (h *Host) leave(s *session) {
	h.mu.Lock()
	defer h.mu.Unlock()
	s.calls--
	s.lastUsed = h.config.Now()
	if !s.configured && s.calls == 0 {
		if current, ok := h.sessions[s.key]; ok && current == s {
			h.end(s)
		}
	}
}

func (h *Host) sweep() {
	for _, s := range h.sessions {
		if h.isIdle(s) {
			h.end(s)
		}
	}
}

func (h *Host) isIdle(s *session) bool {
	return s.calls == 0 && h.config.Now().Sub(s.lastUsed) >= h.config.IdleAfter
}

func (h *Host) endCurrent(s *session) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if current, ok := h.sessions[s.key]; ok && current == s && s.calls == 0 {
		h.end(s)
	}
}

func (h *Host) end(s *session) {
	delete(h.sessions, s.key)
	_ = os.RemoveAll(s.dir)
}

func (h *Host) count(which func(*session) bool) int {
	n := 0
	for _, s := range h.sessions {
		if which(s) {
			n++
		}
	}
	return n
}

func (h *Host) evictIdle(which func(*session) bool) bool {
	var oldest *session
	for _, s := range h.sessions {
		if which(s) && s.calls == 0 && (oldest == nil || s.lastUsed.Before(oldest.lastUsed)) {
			oldest = s
		}
	}
	if oldest == nil {
		return false
	}
	h.end(oldest)
	return true
}

func (h *Host) makeRoom() error {
	all := func(*session) bool { return true }
	if h.count(all) >= h.config.MaxSessions && !h.evictIdle(all) {
		return connect.NewError(connect.CodeResourceExhausted, errors.New("this endpoint holds as many provider sessions in a call as it can: try again shortly"))
	}
	return nil
}
