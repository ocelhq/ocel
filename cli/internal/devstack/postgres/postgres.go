package postgres

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"google.golang.org/protobuf/encoding/protojson"

	"github.com/ocelhq/ocel/cli/internal/declare"
	"github.com/ocelhq/ocel/cli/internal/devstack/docker"
	"github.com/ocelhq/ocel/cli/internal/resolve"
	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
)

const (
	component  = "postgres"
	superuser  = "postgres"
	serverPort = 5432
	dataPath   = "/var/lib/postgresql/data"
	readyIn    = 2 * time.Minute
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
}

type Component struct {
	open docker.Opener

	mu      sync.Mutex
	engine  docker.Engine
	servers map[string]*server
}

func New(open docker.Opener) *Component {
	return &Component{open: open, servers: map[string]*server{}}
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
		held, err := c.server(ctx, project, version)
		if err != nil {
			return nil, err
		}
		if err := c.ensureDatabase(ctx, held, resource.Name); err != nil {
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

func (c *Component) server(ctx context.Context, project, version string) (*server, error) {
	if held, running := c.servers[version]; running {
		return held, nil
	}
	if c.engine == nil {
		engine, err := c.open(ctx)
		if err != nil {
			return nil, err
		}
		c.engine = engine
	}

	password, err := newPassword()
	if err != nil {
		return nil, err
	}
	name := docker.Name(project, component, version)
	container, err := c.engine.Run(ctx, docker.Spec{
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
	held := &server{container: container, password: password}
	if err := c.prepare(ctx, held, version); err != nil {
		_ = c.engine.Stop(ctx, container.ID)
		return nil, err
	}
	c.servers[version] = held
	return held, nil
}

func (c *Component) prepare(ctx context.Context, held *server, version string) error {
	container, password := held.container, held.password
	err := docker.WaitReady(ctx, readyIn, func(ctx context.Context) error {
		_, err := c.engine.Exec(ctx, container.ID, "pg_isready", "-h", "127.0.0.1", "-U", superuser)
		return err
	})
	if err != nil {
		return fmt.Errorf("postgres %s never accepted a connection: %w", version, err)
	}
	if _, err := c.engine.Exec(ctx, container.ID, "psql", "-U", superuser, "-v", "ON_ERROR_STOP=1", "-c", "ALTER USER "+superuser+" PASSWORD '"+password+"'"); err != nil {
		return fmt.Errorf("set this run's postgres password: %w", err)
	}
	return nil
}

func newPassword() (string, error) {
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate a postgres password: %w", err)
	}
	return hex.EncodeToString(raw), nil
}

func (c *Component) ensureDatabase(ctx context.Context, held *server, name string) error {
	listed, err := c.engine.Exec(ctx, held.container.ID, "psql", "-U", superuser, "-tA", "-c", "SELECT datname FROM pg_database")
	if err != nil {
		return fmt.Errorf("list postgres databases: %w", err)
	}
	if slices.Contains(strings.Split(strings.TrimSpace(listed), "\n"), name) {
		return nil
	}
	if _, err := c.engine.Exec(ctx, held.container.ID, "createdb", "-U", superuser, "--", name); err != nil {
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
	key, err := resolve.EnvName(resource.Type, resource.Name)
	if err != nil {
		return resolve.Resource{}, err
	}
	value, err := protojson.Marshal(&bindingsv1.Binding{
		Name: resource.Name,
		Properties: &bindingsv1.Binding_Postgres{Postgres: &bindingsv1.PostgresProperties{
			Host: host, Port: int32(port), Database: resource.Name, Username: superuser, Password: held.password,
		}},
	})
	if err != nil {
		return resolve.Resource{}, err
	}
	var stable bytes.Buffer
	if err := json.Compact(&stable, value); err != nil {
		return resolve.Resource{}, err
	}
	return resolve.Resource{Name: resource.Name, Type: resource.Type, Env: map[string]string{key: stable.String()}}, nil
}

func (c *Component) Close(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	var failed []error
	for version, held := range c.servers {
		if err := c.engine.Stop(ctx, held.container.ID); err != nil {
			failed = append(failed, err)
		}
		delete(c.servers, version)
	}
	return errors.Join(failed...)
}
