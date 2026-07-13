package cli

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"

	"connectrpc.com/connect"

	"github.com/ocelhq/ocel/pkg/channel"
	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
	"github.com/ocelhq/ocel/pkg/proto/deployments/v1/deploymentsv1connect"
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

// fakeInfraClassEnvVar / fakeInfraPresentEnvVar configure the fake provider's
// Preflight response: the class it reports its account points at ("preview",
// "production", "development", or "" for unspecified) and whether the
// infrastructure exists ("0" means absent; anything else means present).
const (
	fakeInfraClassEnvVar   = "OCEL_TEST_FAKE_INFRA_CLASS"
	fakeInfraPresentEnvVar = "OCEL_TEST_FAKE_INFRA_PRESENT"
)

// runDeployFakeProvider binds a Unix socket, prints the readiness sentinel,
// and serves DeploymentService.Deploy: it rejects a missing/mismatched
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
	path, handler := deploymentsv1connect.NewDeploymentServiceHandler(&deployFakeProviderServer{
		token: os.Getenv(channel.SessionTokenEnvVar),
		mode:  os.Getenv(deployFakeProviderModeEnvVar),
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

// deployFakeProviderServer implements deploymentsv1connect.DeploymentServiceHandler
// for TestRunDeploy_HappyPath.
type deployFakeProviderServer struct {
	deploymentsv1connect.UnimplementedDeploymentServiceHandler
	token string
	mode  string
}

func (s *deployFakeProviderServer) Deploy(ctx context.Context, req *deploymentsv1.DeployRequest, stream *connect.ServerStream[deploymentsv1.DeployEvent]) error {
	if err := s.checkToken(ctx); err != nil {
		return err
	}

	if err := validateFixtureManifest(req.GetManifest()); err != nil {
		return stream.Send(&deploymentsv1.DeployEvent{
			Event: &deploymentsv1.DeployEvent_Result{Result: &deploymentsv1.ResultEvent{Success: false, Error: err.Error()}},
		})
	}

	// Echo the received Environment so tests can assert what the CLI resolved
	// and sent, proving `ocel preview`/`ocel deploy` diverge only by it.
	if err := stream.Send(&deploymentsv1.DeployEvent{
		Event: &deploymentsv1.DeployEvent_Progress{Progress: &deploymentsv1.ProgressEvent{Message: "DEPLOY " + describeEnv(req.GetEnvironment())}},
	}); err != nil {
		return err
	}

	// Echo each received function so tests can assert the manifest built by
	// runDeploy actually carries the apps' functions alongside its resources.
	for _, f := range req.GetManifest().GetFunctions() {
		if err := stream.Send(&deploymentsv1.DeployEvent{
			Event: &deploymentsv1.DeployEvent_Progress{Progress: &deploymentsv1.ProgressEvent{Message: "FUNCTION " + describeFunction(f)}},
		}); err != nil {
			return err
		}
	}

	if err := stream.Send(&deploymentsv1.DeployEvent{
		Event: &deploymentsv1.DeployEvent_Progress{Progress: &deploymentsv1.ProgressEvent{Message: "provisioning..."}},
	}); err != nil {
		return err
	}

	if s.mode == "fail" {
		return stream.Send(&deploymentsv1.DeployEvent{
			Event: &deploymentsv1.DeployEvent_Result{Result: &deploymentsv1.ResultEvent{Success: false, Error: "simulated deploy failure"}},
		})
	}
	return stream.Send(&deploymentsv1.DeployEvent{
		Event: &deploymentsv1.DeployEvent_Result{Result: &deploymentsv1.ResultEvent{Success: true}},
	})
}

// Bootstrap satisfies the DeploymentService handler interface. This fake exists
// to exercise the deploy path; the bootstrap path has its own coverage.
func (s *deployFakeProviderServer) Bootstrap(ctx context.Context, req *deploymentsv1.BootstrapRequest, stream *connect.ServerStream[deploymentsv1.DeployEvent]) error {
	return connect.NewError(connect.CodeUnimplemented, errors.New("fake provider does not implement Bootstrap"))
}

// Preflight reports the account's stamped class and whether the infrastructure
// exists, both configured via env so tests can drive the CLI's preflight guard.
func (s *deployFakeProviderServer) Preflight(ctx context.Context, req *deploymentsv1.PreflightRequest) (*deploymentsv1.PreflightResponse, error) {
	if err := s.checkToken(ctx); err != nil {
		return nil, err
	}
	return &deploymentsv1.PreflightResponse{
		InfraClass:            parseInfraClass(os.Getenv(fakeInfraClassEnvVar)),
		InfrastructurePresent: os.Getenv(fakeInfraPresentEnvVar) != "0",
	}, nil
}

// Destroy echoes the Environment it was addressed with (so tests can assert the
// CLI resolved the right teardown target) and streams a terminal success.
func (s *deployFakeProviderServer) Destroy(ctx context.Context, req *deploymentsv1.DestroyRequest, stream *connect.ServerStream[deploymentsv1.DeployEvent]) error {
	if err := s.checkToken(ctx); err != nil {
		return err
	}
	if err := stream.Send(&deploymentsv1.DeployEvent{
		Event: &deploymentsv1.DeployEvent_Progress{Progress: &deploymentsv1.ProgressEvent{Message: "DESTROY project=" + req.GetProjectId() + " " + describeEnv(req.GetEnvironment())}},
	}); err != nil {
		return err
	}
	return stream.Send(&deploymentsv1.DeployEvent{
		Event: &deploymentsv1.DeployEvent_Result{Result: &deploymentsv1.ResultEvent{Success: true}},
	})
}

// ListEnvironments echoes the project_id it was scoped to as a synthetic first
// entry (so tests can assert the CLI sent it), then returns a canned set of
// preview environments for `ocel preview ls` to render.
func (s *deployFakeProviderServer) ListEnvironments(ctx context.Context, req *deploymentsv1.ListEnvironmentsRequest) (*deploymentsv1.ListEnvironmentsResponse, error) {
	if err := s.checkToken(ctx); err != nil {
		return nil, err
	}
	return &deploymentsv1.ListEnvironmentsResponse{
		Environments: []*deploymentsv1.PreviewEnvironment{
			{
				Identity:  "project:" + req.GetProjectId(),
				Lifecycle: deploymentsv1.Environment_LIFECYCLE_EPHEMERAL,
			},
			{
				Identity:  "feature_login_ab12cd34",
				Lifecycle: deploymentsv1.Environment_LIFECYCLE_EPHEMERAL,
				Label:     "pr-7",
				CreatedAt: 1700000000,
			},
			{
				Identity:  "staging",
				Lifecycle: deploymentsv1.Environment_LIFECYCLE_PERSISTENT,
			},
		},
	}, nil
}

// checkToken enforces the session token handshake on a handler call.
func (s *deployFakeProviderServer) checkToken(ctx context.Context) error {
	info, _ := connect.CallInfoForHandlerContext(ctx)
	var authHeader string
	if info != nil {
		authHeader = info.RequestHeader().Get("Authorization")
	}
	if token, ok := channel.ParseAuthHeader(authHeader); !ok || token != s.token {
		return connect.NewError(connect.CodeUnauthenticated, errors.New("bad or missing session token"))
	}
	return nil
}

// describeEnv renders an Environment into a stable, assertable one-line string.
func describeEnv(env *deploymentsv1.Environment) string {
	return fmt.Sprintf("class=%s lifecycle=%s identity=%s source=%s label=%s",
		env.GetClass(), env.GetLifecycle(), env.GetIdentity(), env.GetIdentitySource(), env.GetLabel())
}

// describeFunction renders a ManifestFunction into a stable, assertable
// one-line string carrying every field the manifest should preserve.
func describeFunction(f *deploymentsv1.ManifestFunction) string {
	return fmt.Sprintf("logical_name=%s runtime=%s handler=%s artifact_path=%s framework=%s",
		f.GetLogicalName(), f.GetRuntime(), f.GetHandler(), f.GetArtifactPath(), f.GetFramework())
}

// parseInfraClass maps the fakeInfraClassEnvVar value to an Environment_Class.
func parseInfraClass(s string) deploymentsv1.Environment_Class {
	switch s {
	case "preview":
		return deploymentsv1.Environment_CLASS_PREVIEW
	case "production":
		return deploymentsv1.Environment_CLASS_PRODUCTION
	case "development":
		return deploymentsv1.Environment_CLASS_DEVELOPMENT
	default:
		return deploymentsv1.Environment_CLASS_UNSPECIFIED
	}
}

// validateFixtureManifest confirms the manifest built by runDeploy matches
// what TestRunDeploy_HappyPath's fixture declares: a single postgres
// resource named "main" with its typed config intact.
func validateFixtureManifest(m *deploymentsv1.Manifest) error {
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
