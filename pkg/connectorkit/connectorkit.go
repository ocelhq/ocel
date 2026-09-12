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

	Reveal bool

	Vars providerkit.Vars
}

func (s Spec) capabilities() []string {
	out := []string{CapabilityEnvVarsRead, CapabilityEnvVarsWrite}
	if s.Reveal {
		out = append(out, CapabilityEnvVarsReveal)
	}
	return out
}

// TODO(alpha): the connector answers an unauthenticated port and the console dials it
// directly. #1134 has it polling the console outbound under a pinned Ed25519 identity;
// neither the identity nor the poll transport exists yet.
func Serve(spec Spec) error {
	if spec.Vars.Records == nil {
		return errors.New("connectorkit: Spec.Vars.Records is required")
	}
	if spec.Vars.Sealer == nil {
		return errors.New("connectorkit: Spec.Vars.Sealer is required")
	}

	ln, err := net.Listen("tcp", spec.Addr)
	if err != nil {
		return fmt.Errorf("bind connector listener: %w", err)
	}
	defer ln.Close()

	srv := &http.Server{Handler: Mux(spec)}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	served := make(chan error, 1)
	go func() { served <- srv.Serve(ln) }()

	fmt.Printf("ocel connector %s: %s on http://%s\n", spec.Version, spec.Vendor, ln.Addr())

	select {
	case <-ctx.Done():
		return srv.Close()
	case err := <-served:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	}
}

func Mux(spec Spec) *http.ServeMux {
	mux := http.NewServeMux()

	source := providerkit.StandingVars(spec.Vars)
	path, handler := envvarsv1connect.NewEnvVarsServiceHandler(
		&providerkit.VarsHandler{Source: source},
		connect.WithInterceptors(validate.NewInterceptor()),
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

	return mux
}
