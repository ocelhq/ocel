package postgres

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ocelhq/ocel/cli/internal/declare"
	"github.com/ocelhq/ocel/cli/internal/devstack/docker"
	"github.com/ocelhq/ocel/cli/internal/resolve"
	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
)

const (
	component    = "postgres"
	passwordFile = "postgres-password"
	superuser    = "postgres"
	serverPort   = 5432
	dataPath     = "/var/lib/postgresql/data"
	readyIn      = 2 * time.Minute
)

var images = map[string]string{
	"14": "postgres:14.19@sha256:962ffbe9f6418387643411b127c1db27465e5a23b9a8849bfaf45fa6323963ce",
	"15": "postgres:15.14@sha256:822f8795764a670160640888508b2a68ea5c4b045012c2de17e1d0447bdbdc99",
	"16": "postgres:16.10@sha256:21f6013073bc6b92830a2129570e2f5ec42a6c734b5a985a41e83aa58f54c3c1",
	"17": "postgres:17.6@sha256:00bc86618629af00d2937fdc5a5d63db3ff8450acf52f0636ec813c7f4902929",
}

type server struct {
	container docker.Container
	password  string
	prepared  bool
}

type Component struct {
	open     docker.Opener
	stateDir string

	mu      sync.Mutex
	servers map[string]*server
}

func New(open docker.Opener, stateDir string) *Component {
	return &Component{open: open, stateDir: stateDir, servers: map[string]*server{}}
}

func (c *Component) Resolve(ctx context.Context, project string, resources []declare.Resource) ([]resolve.Resource, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	for _, resource := range resources {
		if _, known := images[resource.Postgres.GetVersion()]; !known {
			return nil, fmt.Errorf("postgres %q asks for version %q, and ocel dev runs %s: declare one of those", resource.Name, resource.Postgres.GetVersion(), strings.Join(versions(), ", "))
		}
	}

	out := make([]resolve.Resource, 0, len(resources))
	for _, resource := range resources {
		version := resource.Postgres.GetVersion()
		engine, err := c.open(ctx)
		if err != nil {
			return nil, err
		}
		held, err := c.server(ctx, engine, project, version)
		if err != nil {
			return nil, err
		}
		if err := ensureDatabase(ctx, engine, held, resource.Name); err != nil {
			return nil, err
		}
		bound, err := bind(resource, held)
		if err != nil {
			return nil, err
		}
		bound.Origin = fmt.Sprintf("postgres:%s @ %s", version, held.container.Addr)
		out = append(out, bound)
	}
	return out, nil
}

func versions() []string {
	known := make([]string, 0, len(images))
	for version := range images {
		known = append(known, version)
	}
	slices.Sort(known)
	return known
}

func (c *Component) server(ctx context.Context, engine docker.Engine, project, version string) (*server, error) {
	held, running := c.servers[version]
	if !running {
		password, err := c.password()
		if err != nil {
			return nil, err
		}
		name := docker.Name(project, component, version)
		container, err := engine.Run(ctx, docker.Spec{
			Name:       name,
			Image:      images[version],
			Env:        []string{"POSTGRES_PASSWORD=" + password},
			Port:       serverPort,
			Volume:     name,
			VolumePath: dataPath,
			Labels:     docker.Labels(project, component),
		})
		if err != nil {
			return nil, err
		}
		held = &server{container: container, password: password}
		c.servers[version] = held
	}
	if !held.prepared {
		if err := prepare(ctx, engine, held, version); err != nil {
			return nil, err
		}
		held.prepared = true
	}
	return held, nil
}

func prepare(ctx context.Context, engine docker.Engine, held *server, version string) error {
	err := docker.WaitReady(ctx, readyIn, func(ctx context.Context) error {
		_, err := engine.Exec(ctx, held.container.ID, "pg_isready", "-h", "127.0.0.1", "-U", superuser)
		return err
	})
	if err != nil {
		return fmt.Errorf("postgres %s never accepted a connection: %w", version, err)
	}
	statement := "ALTER USER " + superuser + " PASSWORD '" + held.password + "';\n"
	if _, err := engine.ExecInput(ctx, held.container.ID, statement, "psql", "-U", superuser, "-v", "ON_ERROR_STOP=1"); err != nil {
		return fmt.Errorf("set this project's postgres password: %s", strings.ReplaceAll(err.Error(), held.password, "<password>"))
	}
	return nil
}

func (c *Component) password() (string, error) {
	path := filepath.Join(c.stateDir, passwordFile)
	for {
		kept, err := os.ReadFile(path)
		if err == nil && len(kept) == 0 {
			return "", fmt.Errorf("%s holds no password: run `ocel dev --reset` to start this project's postgres over", path)
		}
		if err == nil {
			return string(kept), nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("read this project's postgres password: %w", err)
		}
		if err := keepNewPassword(path); err != nil && !errors.Is(err, fs.ErrExist) {
			return "", err
		}
	}
}

func keepNewPassword(path string) error {
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return fmt.Errorf("generate a postgres password: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("keep this project's postgres password: %w", err)
	}
	writing, err := os.CreateTemp(filepath.Dir(path), ".writing-*")
	if err != nil {
		return fmt.Errorf("keep this project's postgres password: %w", err)
	}
	defer func() { _ = os.Remove(writing.Name()) }()
	if _, err := writing.WriteString(hex.EncodeToString(raw)); err != nil {
		_ = writing.Close()
		return fmt.Errorf("keep this project's postgres password: %w", err)
	}
	if err := writing.Close(); err != nil {
		return fmt.Errorf("keep this project's postgres password: %w", err)
	}
	if err := os.Link(writing.Name(), path); err != nil {
		return fmt.Errorf("keep this project's postgres password: %w", err)
	}
	return nil
}

func ensureDatabase(ctx context.Context, engine docker.Engine, held *server, name string) error {
	listed, err := engine.Exec(ctx, held.container.ID, "psql", "-U", superuser, "-tA", "-c", "SELECT datname FROM pg_database")
	if err != nil {
		return fmt.Errorf("list postgres databases: %w", err)
	}
	if slices.Contains(strings.Split(strings.TrimSpace(listed), "\n"), name) {
		return nil
	}
	if _, err := engine.Exec(ctx, held.container.ID, "createdb", "-U", superuser, "--", name); err != nil {
		return fmt.Errorf("create the database for postgres %q: %w", name, err)
	}
	return nil
}

func bind(resource declare.Resource, held *server) (resolve.Resource, error) {
	host, rawPort, err := net.SplitHostPort(held.container.Addr)
	if err != nil {
		return resolve.Resource{}, err
	}
	port, err := strconv.ParseInt(rawPort, 10, 32)
	if err != nil {
		return resolve.Resource{}, err
	}
	return resolve.Bound(resource.Type, &bindingsv1.Binding{
		Name: resource.Name,
		Properties: &bindingsv1.Binding_Postgres{Postgres: &bindingsv1.PostgresProperties{
			Host: host, Port: int32(port), Database: resource.Name, Username: superuser, Password: held.password,
		}},
	})
}

func (c *Component) Close(ctx context.Context, stop bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	held := c.servers
	c.servers = map[string]*server{}
	if !stop || len(held) == 0 {
		return nil
	}
	engine, err := c.open(ctx)
	if err != nil {
		return err
	}
	stopping := make(chan error, len(held))
	for _, one := range held {
		go func() { stopping <- engine.Stop(ctx, one.container.ID) }()
	}
	var failed []error
	for range held {
		if err := <-stopping; err != nil {
			failed = append(failed, err)
		}
	}
	return errors.Join(failed...)
}
