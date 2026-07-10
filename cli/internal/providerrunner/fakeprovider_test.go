package providerrunner

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"time"

	"connectrpc.com/connect"

	providerv1 "github.com/ocelhq/ocel/pkg/proto/provider/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/v1/providerv1connect"
)

// fakeProviderModeEnvVar selects the fake provider's behavior, propagated
// through the child's environment by the test that spawns it.
const fakeProviderModeEnvVar = "OCEL_TEST_FAKE_PROVIDER_MODE"

// fakeProviderSockEnvVar carries the Unix socket path the fake provider
// binds and reports in its readiness sentinel.
const fakeProviderSockEnvVar = "OCEL_TEST_FAKE_PROVIDER_SOCK"

// runFakeProvider is the entry point TestMain dispatches to when this test
// binary is re-exec'd as a fake provider (see testmain_test.go). It never
// returns normally except via the returned exit code.
func runFakeProvider() int {
	mode := os.Getenv(fakeProviderModeEnvVar)

	switch mode {
	case "exit-before-ready":
		fmt.Fprintln(os.Stderr, "fake provider: simulated startup failure")
		return 7
	case "never-ready":
		select {} // blocks until killed; deliberately prints no sentinel
	}

	sockPath := os.Getenv(fakeProviderSockEnvVar)
	if sockPath == "" {
		fmt.Fprintln(os.Stderr, "fake provider: missing socket path")
		return 1
	}
	_ = os.Remove(sockPath)

	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "fake provider: listen:", err)
		return 1
	}
	defer ln.Close()

	mux := http.NewServeMux()
	path, handler := providerv1connect.NewProviderServiceHandler(&fakeProviderServer{
		mode:  mode,
		token: os.Getenv(providerv1.SessionTokenEnvVar),
	})
	mux.Handle(path, handler)

	// Printed only once the listener is bound and the handler mounted, per
	// the readiness sentinel contract.
	fmt.Println(providerv1.FormatReadinessLine(providerv1.FormatUnixAddr(sockPath)))

	srv := &http.Server{Handler: mux}
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return 1
	}
	return 0
}

// fakeProviderServer implements providerv1connect.ProviderServiceHandler
// for tests, driven entirely by mode.
type fakeProviderServer struct {
	mode  string
	token string
}

func (s *fakeProviderServer) Deploy(ctx context.Context, req *providerv1.DeployRequest, stream *connect.ServerStream[providerv1.DeployEvent]) error {
	info, _ := connect.CallInfoForHandlerContext(ctx)
	var authHeader string
	if info != nil {
		authHeader = info.RequestHeader().Get("Authorization")
	}
	if token, ok := providerv1.ParseAuthHeader(authHeader); !ok || token != s.token {
		return connect.NewError(connect.CodeUnauthenticated, errors.New("bad or missing session token"))
	}

	if err := stream.Send(&providerv1.DeployEvent{
		Event: &providerv1.DeployEvent_Progress{Progress: &providerv1.ProgressEvent{Message: "step 1"}},
	}); err != nil {
		return err
	}

	switch s.mode {
	case "fail":
		return stream.Send(&providerv1.DeployEvent{
			Event: &providerv1.DeployEvent_Result{Result: &providerv1.ResultEvent{Success: false, Error: "simulated deploy failure"}},
		})
	case "hang-deploy":
		// Blocks long enough for the test to kill this process mid-call;
		// the runner must observe the broken connection well before this
		// elapses.
		time.Sleep(30 * time.Second)
		return nil
	default: // "success"
		return stream.Send(&providerv1.DeployEvent{
			Event: &providerv1.DeployEvent_Result{Result: &providerv1.ResultEvent{Success: true}},
		})
	}
}
