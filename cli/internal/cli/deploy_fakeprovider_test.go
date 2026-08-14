package cli

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"

	"connectrpc.com/connect"

	"github.com/ocelhq/ocel/pkg/channel"
	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
	"github.com/ocelhq/ocel/pkg/proto/deployments/v1/deploymentsv1connect"
	"github.com/ocelhq/ocel/pkg/proto/env/v1/envv1connect"
)

const deployFakeProviderEnvVar = "OCEL_TEST_DEPLOY_FAKE_PROVIDER"

const deployFakeProviderSockEnvVar = "OCEL_TEST_DEPLOY_FAKE_PROVIDER_SOCK"

const deployFakeProviderModeEnvVar = "OCEL_TEST_DEPLOY_FAKE_PROVIDER_MODE"

const (
	fakeInfraClassEnvVar   = "OCEL_TEST_FAKE_INFRA_CLASS"
	fakeInfraPresentEnvVar = "OCEL_TEST_FAKE_INFRA_PRESENT"
)

const (
	fakeIDAwsAccountEnvVar = "OCEL_TEST_FAKE_AWS_ACCOUNT"
	fakeIDAwsProfileEnvVar = "OCEL_TEST_FAKE_AWS_PROFILE"
	fakeIDAwsRegionEnvVar  = "OCEL_TEST_FAKE_AWS_REGION"
	fakeIDCfAccountEnvVar  = "OCEL_TEST_FAKE_CF_ACCOUNT"
	fakeCredProblemEnvVar  = "OCEL_TEST_FAKE_CRED_PROBLEM"
)

const fakeKnownSlugsEnvVar = "OCEL_TEST_FAKE_KNOWN_SLUGS"

const fakePreflightJournalEnvVar = "OCEL_TEST_FAKE_PREFLIGHT_JOURNAL"

const fakeDomainOwnerEnvVar = "OCEL_TEST_FAKE_DOMAIN_OWNER"

const (
	fakeGlobalDomainEnvVar         = "OCEL_TEST_FAKE_GLOBAL_DOMAIN"
	fakeGlobalDomainAccountEnvVar  = "OCEL_TEST_FAKE_GLOBAL_DOMAIN_CF_ACCOUNT"
	fakeGlobalDomainRouteEnvVar    = "OCEL_TEST_FAKE_GLOBAL_DOMAIN_ROUTE"
	fakeGlobalDomainGrammarEnvVar  = "OCEL_TEST_FAKE_GLOBAL_DOMAIN_GRAMMAR"
	fakeGlobalDomainProjectsEnvVar = "OCEL_TEST_FAKE_GLOBAL_DOMAIN_PROJECTS"
)

const (
	fakeAppURL          = "https://fake-app.example.com"
	fakePromotionID     = "prm_fake_1234"
	fixtureDeploymentID = "3f7c1b9a5e2d4c8f0a6b3d1e7c9f5a2b"
)

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

	fake := &deployFakeProviderServer{
		token: os.Getenv(channel.SessionTokenEnvVar),
		mode:  os.Getenv(deployFakeProviderModeEnvVar),
	}

	mux := http.NewServeMux()
	path, handler := deploymentsv1connect.NewDeploymentServiceHandler(fake)
	mux.Handle(path, handler)

	path, handler = envv1connect.NewEnvVarsServiceHandler(fake)
	mux.Handle(path, handler)

	fmt.Println(channel.FormatReadinessLine(channel.FormatUnixAddr(sockPath)))

	srv := &http.Server{Handler: mux}
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return 1
	}
	return 0
}

type deployFakeProviderServer struct {
	deploymentsv1connect.UnimplementedDeploymentServiceHandler
	token string
	mode  string

	mu               sync.Mutex
	preflightSlug    string
	preflightDomains []string
	preflightClass   deploymentsv1.Environment_Class
}

func (s *deployFakeProviderServer) recordPreflight(slug string, domains []string, class deploymentsv1.Environment_Class) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.preflightSlug, s.preflightDomains, s.preflightClass = slug, domains, class
}

func (s *deployFakeProviderServer) lastPreflight() (string, []string, deploymentsv1.Environment_Class) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.preflightSlug, s.preflightDomains, s.preflightClass
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

	slug, domains, class := s.lastPreflight()
	if err := stream.Send(&deploymentsv1.DeployEvent{
		Event: &deploymentsv1.DeployEvent_Progress{Progress: &deploymentsv1.ProgressEvent{Message: "PREFLIGHT slug=" + slug + " domains=" + strings.Join(domains, ",") + " class=" + class.String()}},
	}); err != nil {
		return err
	}

	if err := stream.Send(&deploymentsv1.DeployEvent{
		Event: &deploymentsv1.DeployEvent_Progress{Progress: &deploymentsv1.ProgressEvent{Message: "DEPLOY " + describeEnv(req.GetEnvironment())}},
	}); err != nil {
		return err
	}

	if err := stream.Send(&deploymentsv1.DeployEvent{
		Event: &deploymentsv1.DeployEvent_Progress{Progress: &deploymentsv1.ProgressEvent{Message: "PROMOTION " + req.GetPromotionId()}},
	}); err != nil {
		return err
	}

	for _, a := range req.GetManifest().GetApps() {
		if err := stream.Send(&deploymentsv1.DeployEvent{
			Event: &deploymentsv1.DeployEvent_Progress{Progress: &deploymentsv1.ProgressEvent{Message: "APP " + describeApp(a)}},
		}); err != nil {
			return err
		}
	}

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
		Event: &deploymentsv1.DeployEvent_Result{Result: &deploymentsv1.ResultEvent{
			Success:     true,
			AppUrls:     []string{fakeAppURL},
			PromotionId: fakePromotionID,
		}},
	})
}

func (s *deployFakeProviderServer) Bootstrap(ctx context.Context, req *deploymentsv1.BootstrapRequest, stream *connect.ServerStream[deploymentsv1.DeployEvent]) error {
	return connect.NewError(connect.CodeUnimplemented, errors.New("fake provider does not implement Bootstrap"))
}

func (s *deployFakeProviderServer) Preflight(ctx context.Context, req *deploymentsv1.PreflightRequest) (*deploymentsv1.PreflightResponse, error) {
	if err := s.checkToken(ctx); err != nil {
		return nil, err
	}
	s.recordPreflight(req.GetSlug(), req.GetDomains(), req.GetRequiredClass())
	journalPreflight(req)
	resp := &deploymentsv1.PreflightResponse{
		InfraClass:            parseInfraClass(os.Getenv(fakeInfraClassEnvVar)),
		InfrastructurePresent: os.Getenv(fakeInfraPresentEnvVar) != "0",
		Identity: &deploymentsv1.Identity{
			AwsAccount:        os.Getenv(fakeIDAwsAccountEnvVar),
			AwsProfile:        os.Getenv(fakeIDAwsProfileEnvVar),
			AwsRegion:         os.Getenv(fakeIDAwsRegionEnvVar),
			CloudflareAccount: os.Getenv(fakeIDCfAccountEnvVar),
		},
	}
	if req.GetSlug() != "" && req.GetRequiredClass() == deploymentsv1.Environment_CLASS_PRODUCTION {
		for _, s := range strings.Split(os.Getenv(fakeKnownSlugsEnvVar), ",") {
			if s = strings.TrimSpace(s); s != "" {
				resp.KnownSlugs = append(resp.KnownSlugs, s)
			}
		}
	}
	owner := os.Getenv(fakeDomainOwnerEnvVar)
	for _, host := range req.GetDomains() {
		claim := &deploymentsv1.DomainClaim{Hostname: host, Status: deploymentsv1.DomainClaim_STATUS_UNCLAIMED}
		if owner != "" {
			claim.Status, claim.Owner = deploymentsv1.DomainClaim_STATUS_CLAIMED, owner
		}
		resp.DomainClaims = append(resp.DomainClaims, claim)
	}
	resp.GlobalPreviewDomain = fakeGlobalDomain()
	if p := os.Getenv(fakeCredProblemEnvVar); p != "" {
		resp.CredentialProblems = append(resp.CredentialProblems, &deploymentsv1.CredentialProblem{
			Provider: p,
			Message:  "could not authenticate",
			Hint:     "configure the credential and re-run",
		})
	}
	return resp, nil
}

func journalPreflight(req *deploymentsv1.PreflightRequest) {
	path := os.Getenv(fakePreflightJournalEnvVar)
	if path == "" {
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		fmt.Fprintln(os.Stderr, "fake provider: preflight journal:", err)
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "slug=%s domains=%s class=%s\n", req.GetSlug(), strings.Join(req.GetDomains(), ","), req.GetRequiredClass())
}

func fakeGlobalDomain() *deploymentsv1.GlobalPreviewDomain {
	base := os.Getenv(fakeGlobalDomainEnvVar)
	if base == "" {
		return nil
	}
	grammarMin, grammarMax := uint32(1), uint32(1)
	if g := os.Getenv(fakeGlobalDomainGrammarEnvVar); g != "" {
		lo, hi, _ := strings.Cut(g, "-")
		grammarMin, grammarMax = parseGrammar(lo), parseGrammar(hi)
	}
	return &deploymentsv1.GlobalPreviewDomain{
		BaseDomain:        base,
		CloudflareAccount: os.Getenv(fakeGlobalDomainAccountEnvVar),
		GrammarMin:        grammarMin,
		GrammarMax:        grammarMax,
		RouteInstalled:    os.Getenv(fakeGlobalDomainRouteEnvVar) != "0",
	}
}

func parseGrammar(s string) uint32 {
	n, err := strconv.ParseUint(strings.TrimSpace(s), 10, 32)
	if err != nil {
		return 0
	}
	return uint32(n)
}

func (s *deployFakeProviderServer) UseDomain(ctx context.Context, req *deploymentsv1.UseDomainRequest, stream *connect.ServerStream[deploymentsv1.DeployEvent]) error {
	if err := s.checkToken(ctx); err != nil {
		return err
	}
	if err := stream.Send(&deploymentsv1.DeployEvent{
		Event: &deploymentsv1.DeployEvent_Progress{Progress: &deploymentsv1.ProgressEvent{Message: "USE DOMAIN class=" + req.GetClass().String() + " base=" + req.GetBaseDomain()}},
	}); err != nil {
		return err
	}
	return stream.Send(&deploymentsv1.DeployEvent{
		Event: &deploymentsv1.DeployEvent_Result{Result: &deploymentsv1.ResultEvent{Success: true}},
	})
}

func (s *deployFakeProviderServer) ReleaseDomain(ctx context.Context, req *deploymentsv1.ReleaseDomainRequest, stream *connect.ServerStream[deploymentsv1.DeployEvent]) error {
	if err := s.checkToken(ctx); err != nil {
		return err
	}
	if err := stream.Send(&deploymentsv1.DeployEvent{
		Event: &deploymentsv1.DeployEvent_Progress{Progress: &deploymentsv1.ProgressEvent{Message: "RELEASE DOMAIN class=" + req.GetClass().String()}},
	}); err != nil {
		return err
	}
	return stream.Send(&deploymentsv1.DeployEvent{
		Event: &deploymentsv1.DeployEvent_Result{Result: &deploymentsv1.ResultEvent{Success: true}},
	})
}

func (s *deployFakeProviderServer) ListDomain(ctx context.Context, req *deploymentsv1.ListDomainRequest) (*deploymentsv1.ListDomainResponse, error) {
	if err := s.checkToken(ctx); err != nil {
		return nil, err
	}
	resp := &deploymentsv1.ListDomainResponse{Domain: fakeGlobalDomain()}
	for _, p := range strings.Split(os.Getenv(fakeGlobalDomainProjectsEnvVar), ",") {
		if p = strings.TrimSpace(p); p != "" {
			resp.Projects = append(resp.Projects, p)
		}
	}
	return resp, nil
}

func (s *deployFakeProviderServer) DestroyPreview(ctx context.Context, req *deploymentsv1.DestroyPreviewRequest, stream *connect.ServerStream[deploymentsv1.DeployEvent]) error {
	if err := s.checkToken(ctx); err != nil {
		return err
	}
	if err := stream.Send(&deploymentsv1.DeployEvent{
		Event: &deploymentsv1.DeployEvent_Progress{Progress: &deploymentsv1.ProgressEvent{Message: "DESTROY project=" + req.GetSlug() + " " + describeEnv(req.GetEnvironment())}},
	}); err != nil {
		return err
	}
	return stream.Send(&deploymentsv1.DeployEvent{
		Event: &deploymentsv1.DeployEvent_Result{Result: &deploymentsv1.ResultEvent{Success: true}},
	})
}

func (s *deployFakeProviderServer) DestroyProject(ctx context.Context, req *deploymentsv1.DestroyProjectRequest, stream *connect.ServerStream[deploymentsv1.DeployEvent]) error {
	if err := s.checkToken(ctx); err != nil {
		return err
	}
	if err := stream.Send(&deploymentsv1.DeployEvent{
		Event: &deploymentsv1.DeployEvent_Progress{Progress: &deploymentsv1.ProgressEvent{Message: "DESTROY PROJECT project=" + req.GetSlug() + " " + describeEnv(req.GetEnvironment())}},
	}); err != nil {
		return err
	}
	return stream.Send(&deploymentsv1.DeployEvent{
		Event: &deploymentsv1.DeployEvent_Result{Result: &deploymentsv1.ResultEvent{Success: true}},
	})
}

const fakeEnvironmentsEnvVar = "OCEL_TEST_FAKE_ENVIRONMENTS"

func (s *deployFakeProviderServer) ListEnvironments(ctx context.Context, req *deploymentsv1.ListEnvironmentsRequest) (*deploymentsv1.ListEnvironmentsResponse, error) {
	if err := s.checkToken(ctx); err != nil {
		return nil, err
	}
	if scripted := os.Getenv(fakeEnvironmentsEnvVar); scripted != "" {
		resp := &deploymentsv1.ListEnvironmentsResponse{}
		if scripted == "none" {
			return resp, nil
		}
		for _, identity := range strings.Split(scripted, ",") {
			resp.Environments = append(resp.Environments, &deploymentsv1.PreviewEnvironment{
				Identity:  identity,
				Lifecycle: deploymentsv1.Environment_LIFECYCLE_PERSISTENT,
			})
		}
		return resp, nil
	}
	return &deploymentsv1.ListEnvironmentsResponse{
		Environments: []*deploymentsv1.PreviewEnvironment{
			{
				Identity:  "project:" + req.GetSlug(),
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

func describeEnv(env *deploymentsv1.Environment) string {
	return fmt.Sprintf("class=%s lifecycle=%s identity=%s source=%s label=%s",
		env.GetClass(), env.GetLifecycle(), env.GetIdentity(), env.GetIdentitySource(), env.GetLabel())
}

func describeFunction(f *deploymentsv1.ManifestFunction) string {
	return fmt.Sprintf("logical_name=%s runtime=%s handler=%s artifact_path=%s framework=%s app=%s",
		f.GetLogicalName(), f.GetRuntime(), f.GetHandler(), f.GetArtifactPath(), f.GetFramework(), f.GetApp())
}

func describeApp(a *deploymentsv1.ManifestApp) string {
	keys := make([]string, 0, len(a.GetVariables()))
	for _, v := range a.GetVariables() {
		keys = append(keys, v.GetKey())
	}
	return fmt.Sprintf("name=%s framework=%s production_domain=%s vars=%s",
		a.GetName(), a.GetFramework(), strings.Join(a.GetDomains()["production"].GetHostnames(), ","), strings.Join(keys, ","))
}

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

func validateFixtureManifest(m *deploymentsv1.Manifest) error {
	if m.GetSchemaVersion() == "" {
		return errors.New("manifest missing schema_version")
	}
	if len(m.GetResources()) != 1 {
		return fmt.Errorf("manifest has %d resources, want 1", len(m.GetResources()))
	}
	r := m.GetResources()[0]
	if r.GetLogicalName() != "db--main" {
		return fmt.Errorf("resource logical_name = %q, want %q", r.GetLogicalName(), "db--main")
	}
	if r.GetPostgres().GetVersion() != "17" {
		return fmt.Errorf("resource postgres version = %q, want %q", r.GetPostgres().GetVersion(), "17")
	}
	return nil
}
