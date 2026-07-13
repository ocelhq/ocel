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

	"github.com/ocelhq/ocel/pkg/channel"
	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
	"github.com/ocelhq/ocel/pkg/proto/deployments/v1/deploymentsv1connect"
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
	path, handler := deploymentsv1connect.NewDeploymentServiceHandler(&fakeProviderServer{
		mode:  mode,
		token: os.Getenv(channel.SessionTokenEnvVar),
	})
	mux.Handle(path, handler)

	// Printed only once the listener is bound and the handler mounted, per
	// the readiness sentinel contract.
	fmt.Println(channel.FormatReadinessLine(channel.FormatUnixAddr(sockPath)))

	srv := &http.Server{Handler: mux}
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return 1
	}
	return 0
}

// fakeProviderServer implements deploymentsv1connect.DeploymentServiceHandler
// for tests, driven entirely by mode.
type fakeProviderServer struct {
	deploymentsv1connect.UnimplementedDeploymentServiceHandler
	mode  string
	token string
}

func (s *fakeProviderServer) Deploy(ctx context.Context, req *deploymentsv1.DeployRequest, stream *connect.ServerStream[deploymentsv1.DeployEvent]) error {
	info, _ := connect.CallInfoForHandlerContext(ctx)
	var authHeader string
	if info != nil {
		authHeader = info.RequestHeader().Get("Authorization")
	}
	if token, ok := channel.ParseAuthHeader(authHeader); !ok || token != s.token {
		return connect.NewError(connect.CodeUnauthenticated, errors.New("bad or missing session token"))
	}

	if err := stream.Send(&deploymentsv1.DeployEvent{
		Event: &deploymentsv1.DeployEvent_Progress{Progress: &deploymentsv1.ProgressEvent{Message: "step 1"}},
	}); err != nil {
		return err
	}

	switch s.mode {
	case "fail":
		return stream.Send(&deploymentsv1.DeployEvent{
			Event: &deploymentsv1.DeployEvent_Result{Result: &deploymentsv1.ResultEvent{Success: false, Error: "simulated deploy failure"}},
		})
	case "hang-deploy":
		// Blocks long enough for the test to kill this process mid-call;
		// the runner must observe the broken connection well before this
		// elapses.
		time.Sleep(30 * time.Second)
		return nil
	default: // "success"
		return stream.Send(&deploymentsv1.DeployEvent{
			Event: &deploymentsv1.DeployEvent_Result{Result: &deploymentsv1.ResultEvent{Success: true}},
		})
	}
}

// Bootstrap mirrors Deploy's auth check and terminal-result behaviour so the
// runner's Bootstrap driver can be exercised the same way. Deploy and
// Bootstrap share one event stream by contract.
func (s *fakeProviderServer) Bootstrap(ctx context.Context, req *deploymentsv1.BootstrapRequest, stream *connect.ServerStream[deploymentsv1.DeployEvent]) error {
	info, _ := connect.CallInfoForHandlerContext(ctx)
	var authHeader string
	if info != nil {
		authHeader = info.RequestHeader().Get("Authorization")
	}
	if token, ok := channel.ParseAuthHeader(authHeader); !ok || token != s.token {
		return connect.NewError(connect.CodeUnauthenticated, errors.New("bad or missing session token"))
	}

	if err := stream.Send(&deploymentsv1.DeployEvent{
		Event: &deploymentsv1.DeployEvent_Progress{Progress: &deploymentsv1.ProgressEvent{Message: "bootstrapping"}},
	}); err != nil {
		return err
	}
	if s.mode == "fail" {
		return stream.Send(&deploymentsv1.DeployEvent{
			Event: &deploymentsv1.DeployEvent_Result{Result: &deploymentsv1.ResultEvent{Success: false, Error: "simulated bootstrap failure"}},
		})
	}
	return stream.Send(&deploymentsv1.DeployEvent{
		Event: &deploymentsv1.DeployEvent_Result{Result: &deploymentsv1.ResultEvent{Success: true}},
	})
}
