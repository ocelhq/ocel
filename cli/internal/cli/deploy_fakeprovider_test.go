package cli

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"

	"connectrpc.com/connect"

	providerv1 "github.com/ocelhq/ocel/pkg/proto/provider/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/v1/providerv1connect"
)

// deployFakeProviderEnvVar, when set to "1" in a re-exec of this test
// binary, tells TestMain to run as a fake provider process instead of the
// real test suite (Go's helper-process pattern, as used by
// providerrunner's own tests). This lets TestRunDeploy_HappyPath drive
// runDeploy's real spawn/ready/deploy/teardown wiring against a real child
// process, without requiring the real @ocel/provider-aws binary.
const deployFakeProviderEnvVar = "OCEL_TEST_DEPLOY_FAKE_PROVIDER"

// deployFakeProviderSockEnvVar carries the Unix socket path the fake
// provider binds and reports in its readiness sentinel.
const deployFakeProviderSockEnvVar = "OCEL_TEST_DEPLOY_FAKE_PROVIDER_SOCK"

// deployFakeProviderModeEnvVar selects the fake provider's Deploy outcome:
// "success" (default) or "fail".
const deployFakeProviderModeEnvVar = "OCEL_TEST_DEPLOY_FAKE_PROVIDER_MODE"

// runDeployFakeProvider binds a Unix socket, prints the readiness sentinel,
// and serves ProviderService.Deploy: it rejects a missing/mismatched
// session token, rejects a manifest that doesn't look like what
// TestRunDeploy_HappyPath's fixture declares (proving the manifest built by
// runDeploy actually reached the provider), then streams a progress event
// followed by a terminal result per deployFakeProviderModeEnvVar.
func runDeployFakeProvider() int {
	sockPath := os.Getenv(deployFakeProviderSockEnvVar)
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
	path, handler := providerv1connect.NewProviderServiceHandler(&deployFakeProviderServer{
		token: os.Getenv(providerv1.SessionTokenEnvVar),
		mode:  os.Getenv(deployFakeProviderModeEnvVar),
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

// deployFakeProviderServer implements providerv1connect.ProviderServiceHandler
// for TestRunDeploy_HappyPath.
type deployFakeProviderServer struct {
	token string
	mode  string
}

func (s *deployFakeProviderServer) Deploy(ctx context.Context, req *providerv1.DeployRequest, stream *connect.ServerStream[providerv1.DeployEvent]) error {
	info, _ := connect.CallInfoForHandlerContext(ctx)
	var authHeader string
	if info != nil {
		authHeader = info.RequestHeader().Get("Authorization")
	}
	if token, ok := providerv1.ParseAuthHeader(authHeader); !ok || token != s.token {
		return connect.NewError(connect.CodeUnauthenticated, errors.New("bad or missing session token"))
	}

	if err := validateFixtureManifest(req.GetManifest()); err != nil {
		return stream.Send(&providerv1.DeployEvent{
			Event: &providerv1.DeployEvent_Result{Result: &providerv1.ResultEvent{Success: false, Error: err.Error()}},
		})
	}

	if err := stream.Send(&providerv1.DeployEvent{
		Event: &providerv1.DeployEvent_Progress{Progress: &providerv1.ProgressEvent{Message: "provisioning..."}},
	}); err != nil {
		return err
	}

	if s.mode == "fail" {
		return stream.Send(&providerv1.DeployEvent{
			Event: &providerv1.DeployEvent_Result{Result: &providerv1.ResultEvent{Success: false, Error: "simulated deploy failure"}},
		})
	}
	return stream.Send(&providerv1.DeployEvent{
		Event: &providerv1.DeployEvent_Result{Result: &providerv1.ResultEvent{Success: true}},
	})
}

// validateFixtureManifest confirms the manifest built by runDeploy matches
// what TestRunDeploy_HappyPath's fixture declares: a single postgres
// resource named "main" with its typed config intact.
func validateFixtureManifest(m *providerv1.Manifest) error {
	if m.GetSchemaVersion() == "" {
		return errors.New("manifest missing schema_version")
	}
	if len(m.GetResources()) != 1 {
		return fmt.Errorf("manifest has %d resources, want 1", len(m.GetResources()))
	}
	r := m.GetResources()[0]
	if r.GetLogicalName() != "postgres_main" {
		return fmt.Errorf("resource logical_name = %q, want %q", r.GetLogicalName(), "postgres_main")
	}
	if r.GetPostgres().GetVersion() != "17" {
		return fmt.Errorf("resource postgres version = %q, want %q", r.GetPostgres().GetVersion(), "17")
	}
	return nil
}
