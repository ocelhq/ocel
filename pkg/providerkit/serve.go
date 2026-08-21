package providerkit

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	connect "connectrpc.com/connect"
	"connectrpc.com/validate"

	"github.com/ocelhq/ocel/pkg/channel"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	"github.com/ocelhq/ocel/pkg/proto/provider/envvars/v1/envvarsv1connect"
)

// Serve is the whole of a provider's main. Binding a listener, reading the
// session token the CLI passed, mounting both services behind the auth, trace and
// validate interceptors, printing the readiness line the CLI waits for, and
// shutting down on a signal are the same on every vendor, so none of them are a
// vendor's to write:
//
//	func main() {
//		if err := providerkit.Serve(aws.New(), version); err != nil {
//			fmt.Fprintln(os.Stderr, "ocel aws provider:", err)
//			os.Exit(1)
//		}
//	}
//
// That is the entire binary. The fifty lines it replaces are in
// platform/aws/provider/cmd/deploy/main.go today, and they are identical to what
// a second provider would have had to copy.
func Serve(p Provider, version string) error {
	token := os.Getenv(channel.SessionTokenEnvVar)
	if token == "" {
		return fmt.Errorf("%s must be set by the launching CLI", channel.SessionTokenEnvVar)
	}

	ln, addr, err := listen()
	if err != nil {
		return fmt.Errorf("bind provider listener: %w", err)
	}
	defer ln.Close()

	fmt.Fprintf(os.Stderr, "ocel %s provider %s: bound %s\n", p.Vendor(), version, addr)

	srv := &http.Server{Handler: NewMux(p, token, version)}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	served := make(chan error, 1)
	go func() { served <- srv.Serve(ln) }()

	fmt.Println(channel.FormatReadinessLine(addr))

	select {
	case <-ctx.Done():
		return srv.Close()
	case err := <-served:
		if err != nil && err != http.ErrServerClosed {
			return err
		}
		return nil
	}
}

// NewMux mounts both services. A provider never sees a route, an interceptor or a
// codec: it hands over its ports and the kit speaks the wire.
func NewMux(p Provider, token, version string) *http.ServeMux {
	mux := http.NewServeMux()
	interceptors := connect.WithInterceptors(
		authInterceptor(token),
		traceInterceptor(),
		validate.NewInterceptor(),
	)

	kit := &handlers{provider: p, version: version}

	path, handler := contractv1connect.NewProviderServiceHandler(kit, interceptors)
	mux.Handle(path, handler)

	path, handler = envvarsv1connect.NewEnvVarsServiceHandler(kit, interceptors)
	mux.Handle(path, handler)

	return mux
}

// handlers is the kit's implementation of both services — all thirty-three RPCs,
// in one place, over the ports. handlers_contract.go and handlers_envvars.go hold
// the method set; this is where the rules that do not vary by vendor live, and it
// is why a vendor writes none of them.
type handlers struct {
	provider Provider
	version  string
}

var (
	_ contractv1connect.ProviderServiceHandler = (*handlers)(nil)
	_ envvarsv1connect.EnvVarsServiceHandler   = (*handlers)(nil)
)

// TODO(#514): these three lift verbatim from platform/aws/provider —
// channelauth.Interceptor, tracecontext.Interceptor, and the unix/windows
// listener pair. They are stubbed here only so the wiring above compiles and can
// be read; assembling the kit moves the real ones.
func authInterceptor(token string) connect.Interceptor { return nil }

func traceInterceptor() connect.Interceptor { return nil }

func listen() (net.Listener, string, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, "", err
	}
	return ln, ln.Addr().String(), nil
}
