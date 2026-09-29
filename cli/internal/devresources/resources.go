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
	"strings"
	"sync"
	"time"

	connect "connectrpc.com/connect"
	"github.com/gofrs/flock"

	"github.com/ocelhq/ocel/cli/internal/declaration"
	"github.com/ocelhq/ocel/cli/internal/devresources/binding"
	"github.com/ocelhq/ocel/cli/internal/devresources/bucket"
	"github.com/ocelhq/ocel/cli/internal/devresources/docker"
	"github.com/ocelhq/ocel/cli/internal/devresources/postgres"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/pkg/naming"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
)

type Backend struct {
	Resolve func(ctx context.Context, project string, resources []declaration.Resource) ([]binding.Resolved, error)
	Routes  func(mux *http.ServeMux, guard func(http.Handler) http.Handler, options ...connect.HandlerOption)
	Close   func(ctx context.Context, stopContainers bool) error
}

type Options struct {
	Open       docker.OpenFunc
	StateDir   string
	AppOrigins func() []string
	Announce   func(line string)
}

var backends = map[resourcesv1.ResourceType]func(Options) Backend{
	resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES: func(opts Options) Backend {
		servers := postgres.New(opts.Open, filepath.Join(opts.StateDir, secretsDir))
		return Backend{Resolve: servers.Resolve, Close: servers.Close}
	},
	resourcesv1.ResourceType_RESOURCE_TYPE_BUCKET: func(opts Options) Backend {
		buckets := bucket.New(opts.Open, filepath.Join(opts.StateDir, secretsDir), opts.AppOrigins)
		return Backend{Resolve: buckets.Resolve, Routes: buckets.Routes, Close: buckets.Close}
	},
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

	backends map[resourcesv1.ResourceType]Backend

	mu        sync.Mutex
	engine    docker.Engine
	usersLock *flock.Flock
	printed   map[string]struct{}
}

func New(project string, opts Options) *Resources {
	if opts.Announce == nil {
		opts.Announce = func(string) {}
	}
	if opts.AppOrigins == nil {
		opts.AppOrigins = func() []string { return nil }
	}
	r := &Resources{project: project, announce: opts.Announce, backends: map[resourcesv1.ResourceType]Backend{}, printed: map[string]struct{}{}}
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
	for kind, build := range backends {
		r.backends[kind] = build(opts)
	}
	return r
}

func ProjectName(dir string) string {
	sum := sha256.Sum256([]byte(filepath.Clean(dir)))
	return strings.Trim(project.DeriveSlug(filepath.Base(dir)), "-") + "-" + hex.EncodeToString(sum[:4])
}

func (r *Resources) Routes(mux *http.ServeMux, guard func(http.Handler) http.Handler, options ...connect.HandlerOption) {
	for _, backend := range r.backends {
		if backend.Routes != nil {
			backend.Routes(mux, guard, options...)
		}
	}
}

func (r *Resources) Resolve(ctx context.Context, resources []declaration.Resource) ([]binding.Resolved, error) {
	var kinds []resourcesv1.ResourceType
	byKind := map[resourcesv1.ResourceType][]declaration.Resource{}
	for _, resource := range resources {
		if _, served := r.backends[resource.Type]; !served {
			return nil, fmt.Errorf("%s %q: ocel dev serves no %s", label(resource.Type), resource.Name, resource.Type)
		}
		if _, seen := byKind[resource.Type]; !seen {
			kinds = append(kinds, resource.Type)
		}
		byKind[resource.Type] = append(byKind[resource.Type], resource)
	}

	var out []binding.Resolved
	for _, kind := range kinds {
		resolved, err := r.backends[kind].Resolve(ctx, r.project, byKind[kind])
		if err != nil {
			var unreachable *docker.Unreachable
			if errors.As(err, &unreachable) {
				needed := *unreachable
				needed.For = fmt.Sprintf("%s %q", label(kind), byKind[kind][0].Name)
				return nil, &needed
			}
			return nil, err
		}
		for _, one := range resolved {
			r.announceOnce(fmt.Sprintf("%s %q → %s", label(kind), one.Name, one.Origin))
		}
		out = append(out, resolved...)
	}
	return out, nil
}

func label(kind resourcesv1.ResourceType) string {
	bound, _ := naming.BindableAs(kind)
	return strings.ToLower(naming.EnvFragment(bound))
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
