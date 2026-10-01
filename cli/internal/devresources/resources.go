package devresources

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	connect "connectrpc.com/connect"
	"github.com/gofrs/flock"

	"github.com/ocelhq/ocel/cli/internal/declaration"
	"github.com/ocelhq/ocel/cli/internal/devresources/binding"
	"github.com/ocelhq/ocel/cli/internal/devresources/bucket"
	"github.com/ocelhq/ocel/cli/internal/devresources/docker"
	"github.com/ocelhq/ocel/cli/internal/devresources/kv"
	"github.com/ocelhq/ocel/cli/internal/devresources/postgres"
	"github.com/ocelhq/ocel/cli/internal/devresources/topic"
	projectpkg "github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/pkg/naming"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
)

type Backend struct {
	Kinds   []resourcesv1.ResourceType
	Resolve func(ctx context.Context, project string, resources []declaration.Resource) ([]binding.Resolved, error)
	Routes  func(mux *http.ServeMux, guard func(http.Handler) http.Handler, options ...connect.HandlerOption)
	Close   func(ctx context.Context, stopContainers bool) error
}

type Options struct {
	Open       docker.OpenFunc
	StateDir   string
	Project    *projectpkg.Project
	AppOrigins func() []string
	Announce   func(line string)
}

const (
	secretsDir            = "secrets"
	usersLockFile         = "users.flock"
	usersLockPollInterval = 100 * time.Millisecond
)

func openUsersLock(stateDir string) (*flock.Flock, error) {
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return nil, fmt.Errorf("record this project's dev state: %w", err)
	}
	return flock.New(filepath.Join(stateDir, usersLockFile)), nil
}

type Resources struct {
	project  string
	announce func(line string)

	backends []Backend
	topics   *topic.Backend

	mu        sync.Mutex
	engine    docker.Engine
	usersLock *flock.Flock
	printed   map[string]struct{}
	resolving map[int]bool
}

func New(project string, opts Options) *Resources {
	if opts.Announce == nil {
		opts.Announce = func(string) {}
	}
	if opts.AppOrigins == nil {
		opts.AppOrigins = func() []string { return nil }
	}
	if opts.Project == nil {
		opts.Project = &projectpkg.Project{}
	}
	r := &Resources{project: project, announce: opts.Announce, printed: map[string]struct{}{}, resolving: map[int]bool{}}
	open := opts.Open
	opts.Open = func(ctx context.Context) (docker.Engine, error) {
		r.mu.Lock()
		defer r.mu.Unlock()
		if r.engine != nil {
			return r.engine, nil
		}
		if r.usersLock == nil {
			lock, err := openUsersLock(opts.StateDir)
			if err != nil {
				return nil, err
			}
			if shared, err := lock.TryRLockContext(ctx, usersLockPollInterval); err != nil || !shared {
				return nil, fmt.Errorf("wait for this project's dev resources to finish stopping: %w", err)
			}
			r.usersLock = lock
		}
		engine, err := open(ctx)
		if err != nil {
			return nil, err
		}
		r.engine = engine
		return r.engine, nil
	}
	secrets := filepath.Join(opts.StateDir, secretsDir)
	servers := postgres.New(opts.Open, secrets)
	stores := kv.New(opts.Open, secrets)
	buckets := bucket.New(opts.Open, secrets, opts.AppOrigins)
	r.topics = topic.New(opts.Open, secrets, opts.Project, opts.Announce)
	r.backends = []Backend{
		{Kinds: []resourcesv1.ResourceType{resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES}, Resolve: servers.Resolve, Close: servers.Close},
		{Kinds: []resourcesv1.ResourceType{resourcesv1.ResourceType_RESOURCE_TYPE_KV}, Resolve: stores.Resolve, Close: stores.Close},
		{Kinds: []resourcesv1.ResourceType{resourcesv1.ResourceType_RESOURCE_TYPE_BUCKET}, Resolve: buckets.Resolve, Routes: buckets.Routes, Close: buckets.Close},
		{Kinds: topic.Kinds, Resolve: r.topics.Resolve, Routes: r.topics.Routes, Close: r.topics.Close},
	}
	return r
}

func ProjectName(dir string) string {
	sum := sha256.Sum256([]byte(filepath.Clean(dir)))
	return strings.Trim(projectpkg.DeriveSlug(filepath.Base(dir)), "-") + "-" + hex.EncodeToString(sum[:4])
}

func (r *Resources) Routes(mux *http.ServeMux, guard func(http.Handler) http.Handler, options ...connect.HandlerOption) {
	for _, backend := range r.backends {
		if backend.Routes != nil {
			backend.Routes(mux, guard, options...)
		}
	}
}

func (r *Resources) Workers() []topic.Worker { return r.topics.Workers() }

func (r *Resources) ServeWorkers(ctx context.Context, urls map[string]string) error {
	return r.topics.Serve(ctx, urls)
}

func (r *Resources) backendOf(kind resourcesv1.ResourceType) (int, bool) {
	for i, backend := range r.backends {
		if slices.Contains(backend.Kinds, kind) {
			return i, true
		}
	}
	return 0, false
}

func (r *Resources) Resolve(ctx context.Context, resources []declaration.Resource) ([]binding.Resolved, error) {
	byBackend := map[int][]declaration.Resource{}
	for _, resource := range resources {
		if refused, ok := refusedInDev[resource.Type]; ok {
			return nil, fmt.Errorf("%s %q: ocel dev does not run %s yet", label(resource.Type), resource.Name, refused)
		}
		i, served := r.backendOf(resource.Type)
		if !served {
			return nil, fmt.Errorf("%s %q: ocel dev serves no %s", label(resource.Type), resource.Name, resource.Type)
		}
		byBackend[i] = append(byBackend[i], resource)
	}

	var out []binding.Resolved
	for i, backend := range r.backends {
		declared, resolvedBefore := byBackend[i], r.isResolving(i)
		if len(declared) == 0 && !resolvedBefore {
			continue
		}
		r.markResolving(i, len(declared) > 0)
		resolved, err := backend.Resolve(ctx, r.project, declared)
		if err != nil {
			var unreachable *docker.Unreachable
			if errors.As(err, &unreachable) && len(declared) > 0 {
				needed := *unreachable
				needed.For = fmt.Sprintf("%s %q", label(declared[0].Type), declared[0].Name)
				return nil, &needed
			}
			return nil, err
		}
		for _, one := range resolved {
			r.announceOnce(fmt.Sprintf("%s %q → %s", label(one.Type), one.Name, one.Origin))
		}
		out = append(out, resolved...)
	}
	return out, nil
}

func (r *Resources) isResolving(backend int) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.resolving[backend]
}

func (r *Resources) markResolving(backend int, declared bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.resolving[backend] = declared
}

var refusedInDev = map[resourcesv1.ResourceType]string{
	// TODO(#1510): ocel dev runs the realtime gateway in-process; until then it refuses a realtime resource.
	resourcesv1.ResourceType_RESOURCE_TYPE_REALTIME: "realtime channels",
}

func label(kind resourcesv1.ResourceType) string {
	return naming.ResourceTypeName(kind)
}

func (r *Resources) announceOnce(line string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, said := r.printed[line]; said {
		return
	}
	r.printed[line] = struct{}{}
	r.announce(line)
}

func (r *Resources) Close(ctx context.Context) error {
	var failed []error
	isLast, err := r.isLastUser()
	if err != nil {
		failed = append(failed, err)
	}

	closing := make(chan error, len(r.backends))
	for _, backend := range r.backends {
		go func() { closing <- backend.Close(ctx, isLast) }()
	}
	for range r.backends {
		if err := <-closing; err != nil {
			failed = append(failed, err)
		}
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.engine != nil {
		if err := r.engine.Close(); err != nil {
			failed = append(failed, err)
		}
		r.engine = nil
	}
	if r.usersLock != nil {
		if err := r.usersLock.Unlock(); err != nil {
			failed = append(failed, err)
		}
		r.usersLock = nil
	}
	return errors.Join(failed...)
}

func (r *Resources) isLastUser() (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.usersLock == nil {
		return true, nil
	}
	if err := r.usersLock.Unlock(); err != nil {
		return false, fmt.Errorf("learn whether another ocel command still uses this project's dev resources, which are left running: %w", err)
	}
	isLast, err := r.usersLock.TryLock()
	if err != nil {
		return false, fmt.Errorf("learn whether another ocel command still uses this project's dev resources, which are left running: %w", err)
	}
	return isLast, nil
}

func Reset(ctx context.Context, open docker.OpenFunc, stateDir, project string) error {
	lock, err := openUsersLock(stateDir)
	if err != nil {
		return err
	}
	isOnlyUser, err := lock.TryLock()
	if err != nil {
		return fmt.Errorf("claim this project's dev resources: %w", err)
	}
	if !isOnlyUser {
		return errors.New("another ocel command is using this project's dev resources: stop it, then run `ocel dev --reset` again")
	}
	defer func() { _ = lock.Unlock() }()

	engine, err := open(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = engine.Close() }()
	if err := engine.Wipe(ctx, docker.ProjectLabels(project)); err != nil {
		return err
	}
	if err := os.RemoveAll(filepath.Join(stateDir, secretsDir)); err != nil {
		return fmt.Errorf("forget the secrets recorded for the wiped resources: %w", err)
	}
	return nil
}
