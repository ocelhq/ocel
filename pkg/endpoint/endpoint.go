package endpoint

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"slices"
	"strings"
	"syscall"

	connect "connectrpc.com/connect"

	"github.com/ocelhq/ocel/pkg/scope"
)

type Mount struct {
	Path    string
	Handler http.Handler

	SelfAuthenticated bool
}

type Spec struct {
	Config

	Version    string
	Vendor     string
	Addr       string
	ConfigPath string
	Identity   Identity

	Verify                  func(ctx context.Context, authorization string) (Caller, error)
	ServesManyOrganizations bool
	Mounts                  []Mount

	VerifyHeartbeat func(*http.Request) error
}

func (s Spec) Services() []string {
	var services []string
	for _, mount := range s.Mounts {
		name := strings.Trim(mount.Path, "/")
		if !scope.IsKnownService(name) || slices.Contains(services, name) {
			continue
		}
		services = append(services, name)
	}
	slices.Sort(services)
	return services
}

func New(spec Spec) (http.Handler, error) {
	if len(spec.Mounts) == 0 {
		return nil, errors.New("endpoint: Spec.Mounts is empty, so this endpoint would serve nothing")
	}
	if spec.EndpointID == "" {
		return nil, errors.New("endpoint: Spec.EndpointID is empty, and a job token is admitted only on the runner it names")
	}
	if spec.OrganizationID == "" && !spec.ServesManyOrganizations {
		return nil, errors.New("endpoint: Spec.OrganizationID is empty: name the organization this endpoint serves, or set ServesManyOrganizations")
	}
	verify := spec.Verify
	if verify == nil {
		tokens, err := NewConsoleTokens(spec.Console, spec.EndpointID, spec.Credentials)
		if err != nil {
			return nil, fmt.Errorf("endpoint: %w", err)
		}
		verify = tokens.Verify
	}
	allowed, err := spec.ListAllowedScopes()
	if err != nil {
		return nil, fmt.Errorf("endpoint: %w", err)
	}
	refused := connect.NewErrorWriter()
	guard := &gate{
		verify:            verify,
		manyOrganizations: spec.ServesManyOrganizations,
		organizationID:    spec.OrganizationID,
		endpointID:        spec.EndpointID,
		allowed:           allowed,
		errors:            refused,
		out:               os.Stdout,
	}

	mux := http.NewServeMux()
	for _, mount := range spec.Mounts {
		if mount.Path == "" || mount.Handler == nil {
			return nil, errors.New("endpoint: a mount names no path or handler")
		}
		if mount.SelfAuthenticated {
			mux.Handle(mount.Path, mount.Handler)
			continue
		}
		if !scope.IsKnownService(strings.Trim(mount.Path, "/")) {
			return nil, fmt.Errorf("endpoint: the mount at %s serves no service, so no method option names the scope its calls need", mount.Path)
		}
		mux.Handle(mount.Path, guard.wrap(mount.Handler))
	}
	if spec.VerifyHeartbeat != nil {
		mux.HandleFunc("POST "+HeartbeatPath, serveHeartbeat(spec))
	}
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		_ = refused.Write(w, r, connect.NewError(connect.CodeUnimplemented, fmt.Errorf("this endpoint serves no %s", r.URL.Path)))
	})
	return mux, nil
}

func Serve(spec Spec) error {
	if spec.KeyPath != "" && !spec.Identity.HasKey() {
		identity, err := LoadOrCreateIdentity(spec.KeyPath)
		if err != nil {
			return err
		}
		spec.Identity = identity
	}

	handler, err := New(spec)
	if err != nil {
		return err
	}

	ln, err := listen(spec.Addr)
	if err != nil {
		return err
	}
	defer ln.Close()

	srv := &http.Server{Handler: handler}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	served := make(chan error, 1)
	go func() { served <- srv.Serve(ln) }()

	unregistered := make(chan struct{})
	if spec.Identity.HasKey() {
		beating, err := beatFor(spec)
		if err != nil {
			return err
		}
		go beating.run(ctx, unregistered)
	}

	fmt.Printf("ocel endpoint %s: %s serving %s on %s %s\n", spec.Version, spec.Vendor, strings.Join(spec.Services(), ", "), ln.Addr().Network(), ln.Addr())

	select {
	case <-ctx.Done():
		return srv.Close()
	case <-unregistered:
		return srv.Close()
	case err := <-served:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	}
}

const socketScheme = "unix://"

const socketMode = 0o660

func listen(addr string) (net.Listener, error) {
	path, socketed := strings.CutPrefix(addr, socketScheme)
	if !socketed {
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			return nil, fmt.Errorf("bind endpoint listener: %w", err)
		}
		return ln, nil
	}
	if path == "" {
		return nil, fmt.Errorf("endpoint: %s names no socket to bind", addr)
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("clear the socket %s a run before this one left: %w", path, err)
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("bind endpoint listener: %w", err)
	}
	if err := os.Chmod(path, socketMode); err != nil {
		ln.Close()
		return nil, fmt.Errorf("set the mode on %s, which the proxy in front of this endpoint connects over: %w", path, err)
	}
	return ln, nil
}

func EnsurePublicKey(configPath string) (string, error) {
	cfg, err := ReadConfig(configPath)
	if err != nil {
		return "", err
	}
	identity, err := LoadOrCreateIdentity(cfg.KeyPath)
	if err != nil {
		return "", err
	}
	return identity.PublicKey(), nil
}
