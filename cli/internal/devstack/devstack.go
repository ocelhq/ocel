package devstack

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"sync"

	connect "connectrpc.com/connect"

	"github.com/ocelhq/ocel/cli/internal/declare"
	"github.com/ocelhq/ocel/cli/internal/devstack/bucket"
	"github.com/ocelhq/ocel/cli/internal/devstack/docker"
	"github.com/ocelhq/ocel/cli/internal/devstack/postgres"
	"github.com/ocelhq/ocel/cli/internal/resolve"
	"github.com/ocelhq/ocel/cli/internal/slug"
	"github.com/ocelhq/ocel/pkg/naming"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
)

type Component interface {
	Resolve(ctx context.Context, project string, resources []declare.Resource) ([]resolve.Resource, error)
	Close(ctx context.Context) error
}

type Router interface {
	Routes(mux *http.ServeMux, guard func(http.Handler) http.Handler, options ...connect.HandlerOption)
}

type Env struct {
	Open       docker.Opener
	AppOrigins []string
	Stdout     io.Writer
	Report     func(error)
}

var table = map[resourcesv1.ResourceType]func(Env) Component{
	resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES: func(env Env) Component { return postgres.New(env.Open) },
	resourcesv1.ResourceType_RESOURCE_TYPE_BUCKET: func(env Env) Component {
		return bucket.New(env.Open, env.AppOrigins, env.Report)
	},
}

func Serves(kind resourcesv1.ResourceType) bool {
	_, served := table[kind]
	return served
}

type Stack struct {
	project string
	stdout  io.Writer

	components map[resourcesv1.ResourceType]Component

	mu      sync.Mutex
	engine  docker.Engine
	printed map[string]struct{}
}

func New(project string, env Env) *Stack {
	if env.Stdout == nil {
		env.Stdout = io.Discard
	}
	s := &Stack{project: project, stdout: env.Stdout, components: map[resourcesv1.ResourceType]Component{}, printed: map[string]struct{}{}}
	open := env.Open
	env.Open = func(ctx context.Context) (docker.Engine, error) {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.engine == nil {
			engine, err := open(ctx)
			if err != nil {
				return nil, err
			}
			s.engine = engine
		}
		return s.engine, nil
	}
	for kind, build := range table {
		s.components[kind] = build(env)
	}
	return s
}

func ProjectName(dir string) string {
	sum := sha256.Sum256([]byte(filepath.Clean(dir)))
	return strings.Trim(slug.From(filepath.Base(dir)), "-") + "-" + hex.EncodeToString(sum[:4])
}

func (s *Stack) Routes(mux *http.ServeMux, guard func(http.Handler) http.Handler, options ...connect.HandlerOption) {
	for _, component := range s.components {
		if router, routes := component.(Router); routes {
			router.Routes(mux, guard, options...)
		}
	}
}

func (s *Stack) Resolve(ctx context.Context, resources []declare.Resource) ([]resolve.Resource, error) {
	var kinds []resourcesv1.ResourceType
	byKind := map[resourcesv1.ResourceType][]declare.Resource{}
	for _, resource := range resources {
		if _, served := s.components[resource.Type]; !served {
			return nil, fmt.Errorf("%s %q: ocel dev serves no %s", label(resource.Type), resource.Name, resource.Type)
		}
		if _, seen := byKind[resource.Type]; !seen {
			kinds = append(kinds, resource.Type)
		}
		byKind[resource.Type] = append(byKind[resource.Type], resource)
	}

	var out []resolve.Resource
	for _, kind := range kinds {
		resolved, err := s.components[kind].Resolve(ctx, s.project, byKind[kind])
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
			s.announce(fmt.Sprintf("%s %q → %s", label(kind), one.Name, one.Origin))
		}
		out = append(out, resolved...)
	}
	return out, nil
}

func label(kind resourcesv1.ResourceType) string {
	bound, _ := naming.BindableAs(kind)
	return strings.ToLower(naming.EnvFragment(bound))
}

func (s *Stack) announce(line string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, said := s.printed[line]; said {
		return
	}
	s.printed[line] = struct{}{}
	fmt.Fprintln(s.stdout, line)
}

func (s *Stack) Close(ctx context.Context) error {
	var failed []error
	for _, component := range s.components {
		if err := component.Close(ctx); err != nil {
			failed = append(failed, err)
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.engine != nil {
		if err := s.engine.Close(); err != nil {
			failed = append(failed, err)
		}
		s.engine = nil
	}
	return errors.Join(failed...)
}

func Reset(ctx context.Context, open docker.Opener, project string) error {
	engine, err := open(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = engine.Close() }()
	return engine.RemoveVolumes(ctx, docker.ProjectLabels(project))
}
