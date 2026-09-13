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
	"syscall"
	"time"

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
	Version string

	Vendor string

	Target string

	Addr string

	Console string

	ConnectorID string

	OrganizationID string

	Grants []string

	Vars providerkit.Vars

	Identity Identity

	KeyPath string

	ConfigPath string
}

func (s Spec) capabilities() []string {
	return s.Grants
}

func Serve(spec Spec) error {
	if spec.Vars.Records == nil {
		return errors.New("connectorkit: Spec.Vars.Records is required")
	}
	if spec.Vars.Sealer == nil {
		return errors.New("connectorkit: Spec.Vars.Sealer is required")
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

	ln, err := net.Listen("tcp", spec.Addr)
	if err != nil {
		return fmt.Errorf("bind connector listener: %w", err)
	}
	defer ln.Close()

	srv := &http.Server{Handler: mux}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	served := make(chan error, 1)
	go func() { served <- srv.Serve(ln) }()

	retired := make(chan struct{})
	if spec.Identity.Held() {
		origin, err := originOf(spec.Console)
		if err != nil {
			return err
		}
		go (&beat{
			origin:       origin,
			connectorID:  spec.ConnectorID,
			version:      spec.Version,
			capabilities: spec.capabilities(),
			identity:     spec.Identity,
			keyPath:      spec.KeyPath,
			configPath:   spec.ConfigPath,
			client:       &http.Client{Timeout: 20 * time.Second},
			every:        beatEvery,
		}).run(ctx, retired)
	}

	fmt.Printf("ocel connector %s: %s on http://%s\n", spec.Version, spec.Vendor, ln.Addr())

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

func Mux(spec Spec) (*http.ServeMux, error) {
	if err := (Config{
		Console:        spec.Console,
		ConnectorID:    spec.ConnectorID,
		OrganizationID: spec.OrganizationID,
		Grants:         spec.Grants,
	}).check(); err != nil {
		return nil, fmt.Errorf("connectorkit: %w", err)
	}

	guard, err := newTrust(spec)
	if err != nil {
		return nil, fmt.Errorf("connectorkit: %w", err)
	}

	mux := http.NewServeMux()

	source := providerkit.StandingVars(spec.Vars)
	path, handler := envvarsv1connect.NewEnvVarsServiceHandler(
		&providerkit.VarsHandler{Source: source},
		connect.WithInterceptors(validate.NewInterceptor(), guard.interceptor()),
	)
	mux.Handle(path, handler)

	mux.HandleFunc("GET /v1/capabilities", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"version":      spec.Version,
			"vendor":       spec.Vendor,
			"target":       spec.Target,
			"capabilities": spec.capabilities(),
		})
	})

	return mux, nil
}
