package postgres

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"

	"github.com/ocelhq/ocel/cli/internal/declare"
	"github.com/ocelhq/ocel/cli/internal/devresources/binding"
	"github.com/ocelhq/ocel/cli/internal/devresources/docker"
	"github.com/ocelhq/ocel/cli/internal/devresources/secret"
	"github.com/ocelhq/ocel/pkg/constants"
	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
)

const (
	backend      = "postgres"
	passwordFile = "postgres-password"
	superuser    = "postgres"
	serverPort   = 5432
	dataPath     = "/var/lib/postgresql/data"
	readyIn      = 2 * time.Minute
)

type server struct {
	container docker.Container
	password  string
	prepared  bool
}

type Backend struct {
	open       docker.OpenFunc
	secretsDir string

	mu      sync.Mutex
	servers map[string]*server
}

func New(open docker.OpenFunc, secretsDir string) *Backend {
	return &Backend{open: open, secretsDir: secretsDir, servers: map[string]*server{}}
}

func (c *Backend) Resolve(ctx context.Context, project string, resources []declare.Resource) ([]binding.Resolved, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	for _, resource := range resources {
		if _, known := constants.PostgresImage(resource.Postgres.GetVersion()); !known {
			return nil, fmt.Errorf("postgres %q asks for version %q, and ocel dev runs %s: declare one of those", resource.Name, resource.Postgres.GetVersion(), strings.Join(constants.PostgresVersions(), ", "))
		}
	}

	out := make([]binding.Resolved, 0, len(resources))
	for _, resource := range resources {
		version := resource.Postgres.GetVersion()
		engine, err := c.open(ctx)
		if err != nil {
			return nil, err
		}
		srv, err := c.server(ctx, engine, project, version)
		if err != nil {
			return nil, err
		}
		if err := ensureDatabase(ctx, engine, srv, resource.Name); err != nil {
			return nil, err
		}
		bound, err := bind(resource, srv)
		if err != nil {
			return nil, err
		}
		bound.Origin = fmt.Sprintf("postgres:%s @ %s", version, srv.container.Address)
		out = append(out, bound)
	}
	return out, nil
}

func (c *Backend) server(ctx context.Context, engine docker.Engine, project, version string) (*server, error) {
	srv, running := c.servers[version]
	if !running {
		password, err := c.password()
		if err != nil {
			return nil, err
		}
		name := docker.Name(project, backend, version)
		image, _ := constants.PostgresImage(version)
		container, err := engine.Run(ctx, docker.Spec{
			Name:       name,
			Image:      image,
			Env:        []string{"POSTGRES_PASSWORD=" + password},
			Port:       serverPort,
			Volume:     name,
			VolumePath: dataPath,
			Labels:     docker.Labels(project, backend),
		})
		if err != nil {
			return nil, err
		}
		srv = &server{container: container, password: password}
		c.servers[version] = srv
	}
	if !srv.prepared {
		if err := prepare(ctx, engine, srv, version); err != nil {
			return nil, err
		}
		srv.prepared = true
	}
	return srv, nil
}

func prepare(ctx context.Context, engine docker.Engine, srv *server, version string) error {
	err := docker.WaitReady(ctx, readyIn, func(ctx context.Context) error {
		_, err := engine.Exec(ctx, srv.container.ID, "pg_isready", "-h", "127.0.0.1", "-U", superuser)
		return err
	})
	if err != nil {
		return fmt.Errorf("postgres %s never accepted a connection: %w", version, err)
	}
	statement := "ALTER USER " + superuser + " PASSWORD '" + srv.password + "';\n"
	if _, err := engine.ExecInput(ctx, srv.container.ID, statement, "psql", "-U", superuser, "-v", "ON_ERROR_STOP=1"); err != nil {
		return fmt.Errorf("set this project's postgres password: %s", strings.ReplaceAll(err.Error(), srv.password, "<password>"))
	}
	return nil
}

func (c *Backend) password() (string, error) {
	return secret.Ensure(filepath.Join(c.secretsDir, passwordFile), secret.Purpose{Owner: "postgres", Noun: "password"})
}

func ensureDatabase(ctx context.Context, engine docker.Engine, srv *server, name string) error {
	listed, err := engine.Exec(ctx, srv.container.ID, "psql", "-U", superuser, "-tA", "-c", "SELECT datname FROM pg_database")
	if err != nil {
		return fmt.Errorf("list postgres databases: %w", err)
	}
	if slices.Contains(strings.Split(strings.TrimSpace(listed), "\n"), name) {
		return nil
	}
	if _, err := engine.Exec(ctx, srv.container.ID, "createdb", "-U", superuser, "--", name); err != nil {
		return fmt.Errorf("create the database for postgres %q: %w", name, err)
	}
	return nil
}

func bind(resource declare.Resource, srv *server) (binding.Resolved, error) {
	host, rawPort, err := net.SplitHostPort(srv.container.Address)
	if err != nil {
		return binding.Resolved{}, err
	}
	port, err := strconv.ParseInt(rawPort, 10, 32)
	if err != nil {
		return binding.Resolved{}, err
	}
	return binding.Encode(resource.Type, &bindingsv1.Binding{
		Name: resource.Name,
		Properties: &bindingsv1.Binding_Postgres{Postgres: &bindingsv1.PostgresProperties{
			Host: host, Port: int32(port), Database: resource.Name, Username: superuser, Password: srv.password,
		}},
	})
}

func (c *Backend) Routes(*http.ServeMux, func(http.Handler) http.Handler, ...connect.HandlerOption) {}

func (c *Backend) Close(ctx context.Context, stopContainers bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	servers := c.servers
	c.servers = map[string]*server{}
	if !stopContainers || len(servers) == 0 {
		return nil
	}
	engine, err := c.open(ctx)
	if err != nil {
		return err
	}
	stopping := make(chan error, len(servers))
	for _, one := range servers {
		go func() { stopping <- engine.Stop(ctx, one.container.ID) }()
	}
	var failed []error
	for range servers {
		if err := <-stopping; err != nil {
			failed = append(failed, err)
		}
	}
	return errors.Join(failed...)
}
