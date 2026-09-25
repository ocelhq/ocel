package connectorkit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"

	connect "connectrpc.com/connect"
	"connectrpc.com/validate"

	"github.com/ocelhq/ocel/pkg/proto/provider/envvars/v1/envvarsv1connect"
	"github.com/ocelhq/ocel/pkg/providerkit"
)

const (
	CapabilityEnvVarsRead   = "envvars.read"
	CapabilityEnvVarsWrite  = "envvars.write"
	CapabilityEnvVarsReveal = "envvars.reveal"
)

type Spec struct {
	Config

	Version string

	Vendor string

	Addr string

	Vars providerkit.Vars

	Identity Identity

	ConfigPath string
}

func Serve(spec Spec) error {
	if spec.Vars.Records == nil {
		return errors.New("connectorkit: Spec.Vars.Records is required")
	}
	if spec.Vars.Cipher == nil {
		return errors.New("connectorkit: Spec.Vars.Cipher is required")
	}

	if spec.KeyPath != "" && !spec.Identity.Held() {
		held, err := LoadOrCreateIdentity(spec.KeyPath)
		if err != nil {
			return err
		}
		spec.Identity = held
	}

	mux, err := Mux(spec)
	if err != nil {
		return err
	}

	ln, err := listen(spec.Addr)
	if err != nil {
		return err
	}
	defer ln.Close()

	srv := &http.Server{Handler: mux}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	served := make(chan error, 1)
	go func() { served <- srv.Serve(ln) }()

	retired := make(chan struct{})
	if spec.Identity.Held() {
		beating, err := beatFor(spec)
		if err != nil {
			return err
		}
		go beating.run(ctx, retired)
	}

	fmt.Printf("ocel connector %s: %s on %s %s\n", spec.Version, spec.Vendor, ln.Addr().Network(), ln.Addr())

	select {
	case <-ctx.Done():
		return srv.Close()
	case <-retired:
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
			return nil, fmt.Errorf("bind connector listener: %w", err)
		}
		return ln, nil
	}
	if path == "" {
		return nil, fmt.Errorf("connectorkit: %s names no socket to bind", addr)
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("clear the socket %s a run before this one left: %w", path, err)
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("bind connector listener: %w", err)
	}
	if err := os.Chmod(path, socketMode); err != nil {
		ln.Close()
		return nil, fmt.Errorf("set the mode on %s, which the proxy in front of this connector connects over: %w", path, err)
	}
	return ln, nil
}

func PublicKey(configPath string) (string, error) {
	cfg, err := ReadConfig(configPath)
	if err != nil {
		return "", err
	}
	held, err := LoadOrCreateIdentity(cfg.KeyPath)
	if err != nil {
		return "", err
	}
	return held.PublicKey(), nil
}

func Mux(spec Spec) (*http.ServeMux, error) {
	if err := spec.check(); err != nil {
		return nil, fmt.Errorf("connectorkit: %w", err)
	}

	guard, err := newTrust(spec)
	if err != nil {
		return nil, fmt.Errorf("connectorkit: %w", err)
	}

	mux := http.NewServeMux()

	source := providerkit.FixedVars(spec.Vars)
	path, handler := envvarsv1connect.NewEnvVarsServiceHandler(
		&providerkit.VarsService{Source: source},
		connect.WithInterceptors(validate.NewInterceptor(), guard.interceptor()),
	)
	mux.Handle(path, handler)

	mux.HandleFunc("GET /v1/capabilities", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if _, err := guard.caller(r.Context(), r.Header.Get("Authorization")); err != nil {
			w.WriteHeader(refusedStatus(err))
			_ = json.NewEncoder(w).Encode(map[string]any{"error": err.Error()})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"version":      spec.Version,
			"vendor":       spec.Vendor,
			"target":       spec.Target,
			"capabilities": spec.Grants,
		})
	})

	return mux, nil
}

func refusedStatus(err error) int {
	if connect.CodeOf(err) == connect.CodePermissionDenied {
		return http.StatusForbidden
	}
	return http.StatusUnauthorized
}
