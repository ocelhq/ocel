package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"

	"connectrpc.com/connect"

	"github.com/ocelhq/ocel/pkg/channel"
	"github.com/ocelhq/ocel/pkg/naming"
	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
	"github.com/ocelhq/ocel/pkg/proto/deployments/v1/deploymentsv1connect"
	"github.com/ocelhq/ocel/pkg/proto/env/v1/envv1connect"
	linksv1 "github.com/ocelhq/ocel/pkg/proto/links/v1"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
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

const fakeFlipBoundEnvVar = "OCEL_TEST_FAKE_FLIP_BOUND"

const fakeKnownSlugsEnvVar = "OCEL_TEST_FAKE_KNOWN_SLUGS"

const fakePublishedLinksEnvVar = "OCEL_TEST_FAKE_PUBLISHED_LINKS"

const fakePreflightJournalEnvVar = "OCEL_TEST_FAKE_PREFLIGHT_JOURNAL"

const fakeEdgeJournalEnvVar = "OCEL_TEST_FAKE_EDGE_JOURNAL"

const fakeConfigureJournalEnvVar = "OCEL_TEST_FAKE_CONFIGURE_JOURNAL"

const fakeEnabledFeaturesEnvVar = "OCEL_TEST_FAKE_ENABLED_FEATURES"

const fakeEdgeRefusalEnvVar = "OCEL_TEST_FAKE_EDGE_REFUSAL"

const fakeNeedsRefusalEnvVar = "OCEL_TEST_FAKE_NEEDS_REFUSAL"

const fakeDegradedEnvVar = "OCEL_TEST_FAKE_DEGRADED"

const fakeDomainOwnerEnvVar = "OCEL_TEST_FAKE_DOMAIN_OWNER"

const (
	fakeGlobalDomainEnvVar         = "OCEL_TEST_FAKE_GLOBAL_DOMAIN"
	fakeGlobalDomainAccountEnvVar  = "OCEL_TEST_FAKE_GLOBAL_DOMAIN_CF_ACCOUNT"
	fakeGlobalDomainRouteEnvVar    = "OCEL_TEST_FAKE_GLOBAL_DOMAIN_ROUTE"
	fakeGlobalDomainGrammarEnvVar  = "OCEL_TEST_FAKE_GLOBAL_DOMAIN_GRAMMAR"
	fakeGlobalDomainProjectsEnvVar = "OCEL_TEST_FAKE_GLOBAL_DOMAIN_PROJECTS"
	fakeGlobalDomainCertEnvVar     = "OCEL_TEST_FAKE_GLOBAL_DOMAIN_CERT"
	fakeGlobalDomainRecordsEnvVar  = "OCEL_TEST_FAKE_GLOBAL_DOMAIN_RECORDS"
	fakeGlobalDomainOwedEnvVar     = "OCEL_TEST_FAKE_GLOBAL_DOMAIN_OWED"
	fakeGlobalDomainProbeEnvVar    = "OCEL_TEST_FAKE_GLOBAL_DOMAIN_PROBE"
)

const (
	fakeAppURL      = "https://fake-app.example.com"
	fakePromotionID = "prm_fake_1234"
	fakeLinkSecret  = "pw-do-not-publish-9f2c"
)

func fixtureDeploymentID(app string) string {
	sum := sha256.Sum256([]byte("ocel-test-deployment/" + app))
	return hex.EncodeToString(sum[:])[:32]
}

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

	mu                sync.Mutex
	domainStatusCalls int
	preflightSlug     string
	preflightDomains  []string
	preflightClass    deploymentsv1.Environment_Class
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

	journalEdge(req.GetEdge().GetKind(), req.GetEdge().GetDns(), req.GetEdge().GetAllowDegraded())
	if err := refuseEdge(); err != nil {
		return err
	}

	if err := validateFixtureManifest(req.GetManifest()); err != nil {
		return stream.Send(&deploymentsv1.DeployEvent{
			Event: &deploymentsv1.DeployEvent_Result{Result: &deploymentsv1.ResultEvent{Success: false, Error: err.Error()}},
		})
	}

	for _, ev := range fakeDegradedEvents() {
		if err := stream.Send(ev); err != nil {
			return err
		}
	}
	if refusal := os.Getenv(fakeNeedsRefusalEnvVar); refusal != "" {
		return stream.Send(&deploymentsv1.DeployEvent{
			Event: &deploymentsv1.DeployEvent_Result{Result: &deploymentsv1.ResultEvent{Success: false, Error: refusal}},
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

	for _, message := range consumeFakeLinks(req.GetManifest()) {
		if err := stream.Send(&deploymentsv1.DeployEvent{
			Event: &deploymentsv1.DeployEvent_Progress{Progress: &deploymentsv1.ProgressEvent{Message: message}},
		}); err != nil {
			return err
		}
	}
	if refusal := refuseUnpublishedFakeLinks(req.GetManifest()); refusal != "" {
		return stream.Send(&deploymentsv1.DeployEvent{
			Event: &deploymentsv1.DeployEvent_Result{Result: &deploymentsv1.ResultEvent{Success: false, Error: refusal}},
		})
	}

	for _, u := range req.GetManifest().GetUsages() {
		if err := stream.Send(&deploymentsv1.DeployEvent{
			Event: &deploymentsv1.DeployEvent_Progress{Progress: &deploymentsv1.ProgressEvent{Message: "USAGE " + describeUsage(u)}},
		}); err != nil {
			return err
		}
	}

	for _, a := range req.GetManifest().GetApps() {
		if err := stream.Send(&deploymentsv1.DeployEvent{
			Event: &deploymentsv1.DeployEvent_Progress{Progress: &deploymentsv1.ProgressEvent{Message: "DELIVER " + describeDelivery(req.GetManifest(), a.GetName())}},
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
			FlipBound:   fakeFlipBound(),
			Links:       fakeLinks(req.GetManifest()),
		}},
	})
}

func fakeFlipBound() *deploymentsv1.FlipBound {
	spec := os.Getenv(fakeFlipBoundEnvVar)
	if spec == "" {
		return nil
	}
	typical, published, _ := strings.Cut(spec, ":")
	ms, err := strconv.ParseInt(typical, 10, 64)
	if err != nil {
		return nil
	}
	return &deploymentsv1.FlipBound{TypicalMs: ms, Published: published == "published"}
}

func fakePublishedLinks() []string {
	var out []string
	for _, name := range strings.Split(os.Getenv(fakePublishedLinksEnvVar), ",") {
		if name = strings.TrimSpace(name); name != "" {
			out = append(out, name)
		}
	}
	return out
}

func consumeFakeLinks(m *deploymentsv1.Manifest) []string {
	published := fakePublishedLinks()
	var out []string
	for _, r := range m.GetResources() {
		id := r.GetResource().GetName()
		if r.GetLinked() {
			out = append(out, "LINK bound="+r.GetLogicalName()+" name="+id)
			continue
		}
		if slices.Contains(published, id) {
			out = append(out, "LINK shadowed="+r.GetLogicalName()+" name="+id)
		}
	}
	return out
}

func refuseUnpublishedFakeLinks(m *deploymentsv1.Manifest) string {
	published := fakePublishedLinks()
	var missing []string
	for _, r := range m.GetResources() {
		if r.GetLinked() && !slices.Contains(published, r.GetResource().GetName()) {
			missing = append(missing, r.GetResource().GetName())
		}
	}
	if len(missing) == 0 {
		return ""
	}
	return "nothing has published a link named " + strings.Join(missing, ", ") + " to production"
}

func fakeLinks(m *deploymentsv1.Manifest) []*linksv1.Link {
	out := make([]*linksv1.Link, 0, len(m.GetResources()))
	for _, r := range m.GetResources() {
		if r.GetLinked() {
			continue
		}
		link := &linksv1.Link{
			Name: r.GetLogicalName(),
			Grants: []*linksv1.Grant{{
				Actions:   []string{"fake:connect"},
				Resources: []string{"fake:resource/main"},
				Label:     "connect",
			}},
		}
		switch r.GetResource().GetType() {
		case linksv1.LinkType_LINK_TYPE_BUCKET:
			link.Properties = &linksv1.Link_Bucket{Bucket: &linksv1.BucketProperties{Bucket: r.GetResource().GetName() + "-" + fakeLinkSecret}}
		default:
			link.Properties = &linksv1.Link_Postgres{Postgres: &linksv1.PostgresProperties{
				Host:     "db.fake.internal",
				Port:     5432,
				Username: "app",
				Password: fakeLinkSecret,
				Database: r.GetResource().GetName(),
			}}
		}
		out = append(out, link)
	}
	return out
}

func (s *deployFakeProviderServer) DescribeBootstrap(ctx context.Context, _ *deploymentsv1.DescribeBootstrapRequest) (*deploymentsv1.DescribeBootstrapResponse, error) {
	if err := s.checkToken(ctx); err != nil {
		return nil, err
	}
	enabled := strings.Split(os.Getenv(fakeEnabledFeaturesEnvVar), ",")
	feature := func(name, summary string, dependsOn ...string) *deploymentsv1.Feature {
		return &deploymentsv1.Feature{
			Name:      name,
			Summary:   summary,
			DependsOn: dependsOn,
			Enabled:   slices.Contains(enabled, name),
		}
	}
	return &deploymentsv1.DescribeBootstrapResponse{
		Features: []*deploymentsv1.Feature{
			feature("isr", "incremental static regeneration"),
			feature("image-optimization", "on-demand image optimization"),
			feature("cloudflare-edge", "a Cloudflare front", "isr"),
		},
	}, nil
}

func (s *deployFakeProviderServer) Bootstrap(ctx context.Context, req *deploymentsv1.BootstrapRequest, stream *connect.ServerStream[deploymentsv1.DeployEvent]) error {
	if err := s.checkToken(ctx); err != nil {
		return err
	}
	journalBootstrap(req.GetFeatures(), req.GetForce())
	return stream.Send(&deploymentsv1.DeployEvent{
		Event: &deploymentsv1.DeployEvent_Result{Result: &deploymentsv1.ResultEvent{Success: true}},
	})
}

func (s *deployFakeProviderServer) PlanTeardown(ctx context.Context, req *deploymentsv1.PlanTeardownRequest) (*deploymentsv1.PlanTeardownResponse, error) {
	if err := s.checkToken(ctx); err != nil {
		return nil, err
	}
	journalEdge(req.GetEdge().GetKind(), nil, nil)
	if err := refuseEdge(); err != nil {
		return nil, err
	}
	class := strings.ToLower(strings.TrimPrefix(req.GetClass().String(), "CLASS_"))
	return &deploymentsv1.PlanTeardownResponse{
		EdgeKind: resolvedEdgeKind(req.GetEdge().GetKind()),
		Items: []*deploymentsv1.TeardownItem{
			{
				Kind:   "edge bootstrap",
				Name:   resolvedEdgeKind(req.GetEdge().GetKind()),
				Action: deploymentsv1.TeardownItem_ACTION_DELETE,
				Reason: "every worker the edge stood up for the " + class + " substrate",
			},
			{
				Kind:   "bucket",
				Name:   "ocel-state-" + class,
				Action: deploymentsv1.TeardownItem_ACTION_DELETE,
				Reason: "the Pulumi state of every stack this substrate deployed",
				Slow:   true,
			},
			{
				Kind:   "parameter",
				Name:   "/ocel/pulumi/passphrase",
				Action: deploymentsv1.TeardownItem_ACTION_KEEP,
				Reason: "the production substrate is still bootstrapped and its Pulumi state is encrypted under it",
			},
		},
	}, nil
}

func (s *deployFakeProviderServer) Teardown(ctx context.Context, req *deploymentsv1.TeardownRequest, stream *connect.ServerStream[deploymentsv1.DeployEvent]) error {
	if err := s.checkToken(ctx); err != nil {
		return err
	}
	journalEdge(req.GetEdge().GetKind(), nil, nil)
	if err := refuseEdge(); err != nil {
		return err
	}
	if err := stream.Send(&deploymentsv1.DeployEvent{
		Event: &deploymentsv1.DeployEvent_Progress{Progress: &deploymentsv1.ProgressEvent{Message: "TEARDOWN class=" + req.GetClass().String()}},
	}); err != nil {
		return err
	}
	return stream.Send(&deploymentsv1.DeployEvent{
		Event: &deploymentsv1.DeployEvent_Result{Result: &deploymentsv1.ResultEvent{Success: true}},
	})
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
			AwsAccount: os.Getenv(fakeIDAwsAccountEnvVar),
			AwsProfile: os.Getenv(fakeIDAwsProfileEnvVar),
			AwsRegion:  os.Getenv(fakeIDAwsRegionEnvVar),
			EdgeScope:  os.Getenv(fakeIDCfAccountEnvVar),
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

func resolvedEdgeKind(kind string) string {
	if kind == "" {
		return "cloudfront"
	}
	return kind
}

func journalEdge(kind string, dns *deploymentsv1.Dns, allowDegraded []string) {
	path := os.Getenv(fakeEdgeJournalEnvVar)
	if path == "" {
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		fmt.Fprintln(os.Stderr, "fake provider: edge journal:", err)
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "kind=%s dns=%s/%s allowDegraded=%s\n", kind, dns.GetKind(), dns.GetZone(), strings.Join(allowDegraded, ","))
}

func (s *deployFakeProviderServer) Configure(ctx context.Context, req *deploymentsv1.ConfigureRequest) (*deploymentsv1.ConfigureResponse, error) {
	if err := s.checkToken(ctx); err != nil {
		return nil, err
	}
	path := os.Getenv(fakeConfigureJournalEnvVar)
	if path == "" {
		return &deploymentsv1.ConfigureResponse{}, nil
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	aws := req.GetConfig().GetAws()
	fmt.Fprintf(f, "region=%s transforms=%s certificates=%v\n", aws.GetRegion(), strings.Join(aws.GetTransforms(), ","), aws.GetCertificates())
	return &deploymentsv1.ConfigureResponse{}, nil
}

func journalBootstrap(features []string, force bool) {
	path := os.Getenv(fakeEdgeJournalEnvVar)
	if path == "" {
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		fmt.Fprintln(os.Stderr, "fake provider: edge journal:", err)
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "features=%s force=%t\n", strings.Join(features, ","), force)
}

func fakeDegradedEvents() []*deploymentsv1.DeployEvent {
	var events []*deploymentsv1.DeployEvent
	for _, entry := range strings.Split(os.Getenv(fakeDegradedEnvVar), ";") {
		need, detail, ok := strings.Cut(entry, "=")
		if !ok || need == "" {
			continue
		}
		events = append(events, &deploymentsv1.DeployEvent{
			Event: &deploymentsv1.DeployEvent_Degraded{Degraded: &deploymentsv1.DegradedEvent{Need: need, Detail: detail}},
		})
	}
	return events
}

func refuseEdge() error {
	msg := os.Getenv(fakeEdgeRefusalEnvVar)
	if msg == "" {
		return nil
	}
	return connect.NewError(connect.CodeInvalidArgument, errors.New(msg))
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
	status, certID, _ := strings.Cut(os.Getenv(fakeGlobalDomainCertEnvVar), " ")
	probeAt, probeEdge, probeOK := fakeGlobalDomainProbe()
	return &deploymentsv1.GlobalPreviewDomain{
		BaseDomain:        base,
		EdgeScope:         os.Getenv(fakeGlobalDomainAccountEnvVar),
		GrammarMin:        grammarMin,
		GrammarMax:        grammarMax,
		RouteInstalled:    os.Getenv(fakeGlobalDomainRouteEnvVar) != "0",
		CertificateId:     certID,
		CertificateStatus: status,
		RecordsWritten:    splitList(os.Getenv(fakeGlobalDomainRecordsEnvVar)),
		RecordsOwed:       splitList(os.Getenv(fakeGlobalDomainOwedEnvVar)),
		LastProbeAt:       probeAt,
		LastProbeEdge:     probeEdge,
		LastProbeOk:       probeOK,
	}
}

func splitList(raw string) []string {
	var out []string
	for _, item := range strings.Split(raw, ",") {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}

func fakeGlobalDomainProbe() (int64, string, bool) {
	fields := strings.Fields(os.Getenv(fakeGlobalDomainProbeEnvVar))
	if len(fields) < 2 {
		return 0, "", false
	}
	at, err := strconv.ParseInt(fields[0], 10, 64)
	if err != nil {
		return 0, "", false
	}
	return at, fields[1], len(fields) < 3 || fields[2] != "failed"
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
		Event: &deploymentsv1.DeployEvent_Progress{Progress: &deploymentsv1.ProgressEvent{Message: "USE DOMAIN class=" + req.GetClass().String() + " base=" + req.GetBaseDomain() + " dns=" + req.GetEdge().GetDns().GetKind()}},
	}); err != nil {
		return err
	}
	records, err := edge.RecordsFor(edge.DNSTarget{Kind: "cloudflare", ServesUnbound: true}, []string{"*." + req.GetBaseDomain()})
	if err != nil {
		return err
	}
	for _, rec := range records {
		message := "Writing " + rec.String()
		if req.GetEdge().GetDns() == nil {
			message = rec.Instruction()
		}
		if err := stream.Send(&deploymentsv1.DeployEvent{
			Event: &deploymentsv1.DeployEvent_Progress{Progress: &deploymentsv1.ProgressEvent{Message: message}},
		}); err != nil {
			return err
		}
	}
	return stream.Send(&deploymentsv1.DeployEvent{
		Event: &deploymentsv1.DeployEvent_Result{Result: &deploymentsv1.ResultEvent{Success: true}},
	})
}

const fakeServedPreviewsEnvVar = "OCEL_TEST_FAKE_SERVED_PREVIEWS"

func (s *deployFakeProviderServer) PlanReleaseDomain(ctx context.Context, req *deploymentsv1.PlanReleaseDomainRequest) (*deploymentsv1.PlanReleaseDomainResponse, error) {
	if err := s.checkToken(ctx); err != nil {
		return nil, err
	}
	base := os.Getenv(fakeGlobalDomainEnvVar)
	if base == "" {
		return &deploymentsv1.PlanReleaseDomainResponse{}, nil
	}
	if served := os.Getenv(fakeServedPreviewsEnvVar); served != "" {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf(
			"*.%s still carries live preview pointers for %s — run `ocel preview rm` or `ocel destroy --preview` in each of them first",
			base, served,
		))
	}
	return &deploymentsv1.PlanReleaseDomainResponse{
		BaseDomain: base,
		EdgeStack: &deploymentsv1.EdgeStackPlan{
			EdgeKind: "cloudflare",
			Items: []*deploymentsv1.TeardownItem{
				{
					Kind:   "preview entry worker",
					Name:   "*." + base,
					Action: deploymentsv1.TeardownItem_ACTION_DELETE,
					Reason: "the shared entry worker holding this wildcard",
				},
				{
					Kind:   "DNS record",
					Name:   "*." + base + " CNAME you.example.com",
					Action: deploymentsv1.TeardownItem_ACTION_KEEP,
					Reason: "you created it yourself; ocel never wrote it",
				},
			},
		},
	}, nil
}

func (s *deployFakeProviderServer) ReleaseDomain(ctx context.Context, req *deploymentsv1.ReleaseDomainRequest, stream *connect.ServerStream[deploymentsv1.DeployEvent]) error {
	if err := s.checkToken(ctx); err != nil {
		return err
	}
	if err := stream.Send(&deploymentsv1.DeployEvent{
		Event: &deploymentsv1.DeployEvent_Progress{Progress: &deploymentsv1.ProgressEvent{Message: "RELEASE DOMAIN class=" + req.GetClass().String() + " dns=" + req.GetEdge().GetDns().GetKind()}},
	}); err != nil {
		return err
	}
	return stream.Send(&deploymentsv1.DeployEvent{
		Event: &deploymentsv1.DeployEvent_Result{Result: &deploymentsv1.ResultEvent{Success: true}},
	})
}

const fakeDomainTimeoutEnvVar = "OCEL_TEST_FAKE_DOMAIN_TIMEOUT"

func (s *deployFakeProviderServer) AddDomain(ctx context.Context, req *deploymentsv1.AddDomainRequest, stream *connect.ServerStream[deploymentsv1.DeployEvent]) error {
	if err := s.checkToken(ctx); err != nil {
		return err
	}
	say := func(message string) error {
		return stream.Send(&deploymentsv1.DeployEvent{
			Event: &deploymentsv1.DeployEvent_Progress{Progress: &deploymentsv1.ProgressEvent{Message: message}},
		})
	}
	if host := req.GetHost(); host != "" && !slices.Contains(req.GetConfigured(), host) {
		return stream.Send(&deploymentsv1.DeployEvent{
			Event: &deploymentsv1.DeployEvent_Result{Result: &deploymentsv1.ResultEvent{
				Success: false,
				Error:   fmt.Sprintf("this project does not declare %q: add it to domains.production and run this again — no command edits the config, which declares %s", host, strings.Join(req.GetConfigured(), ", ")),
			}},
		})
	}
	hosts := fakeDomainTargets(req.GetConfigured(), req.GetHost())
	if err := say(fmt.Sprintf("DOMAIN ADD slug=%s hosts=%s dns=%s edge=%s", req.GetSlug(), strings.Join(hosts, ","), req.GetEdge().GetDns().GetKind(), resolvedEdgeKind(req.GetEdge().GetKind()))); err != nil {
		return err
	}
	if err := say(fmt.Sprintf("Requesting a certificate for %s in us-east-1", strings.Join(hosts, ", "))); err != nil {
		return err
	}
	for _, host := range hosts {
		if err := say(fmt.Sprintf("Binding %s to the cloudflare edge", host)); err != nil {
			return err
		}
		records, err := edge.RecordsFor(edge.DNSTarget{Kind: "cloudflare", ServesUnbound: true}, []string{host})
		if err != nil {
			return err
		}
		for _, rec := range records {
			message := "Writing " + rec.String()
			if req.GetEdge().GetDns() == nil {
				message = rec.Instruction()
			}
			if err := say(message); err != nil {
				return err
			}
		}
		if outstanding := os.Getenv(fakeDomainTimeoutEnvVar); outstanding != "" {
			return stream.Send(&deploymentsv1.DeployEvent{
				Event: &deploymentsv1.DeployEvent_Result{Result: &deploymentsv1.ResultEvent{
					Success: false,
					Error:   fmt.Sprintf("gave up after 5m0s waiting for https://%s/ to answer as the cloudflare edge; still outstanding: %s", host, outstanding),
				}},
			})
		}
		if err := say(fmt.Sprintf("%s is served by the cloudflare edge", host)); err != nil {
			return err
		}
	}
	return stream.Send(&deploymentsv1.DeployEvent{
		Event: &deploymentsv1.DeployEvent_Result{Result: &deploymentsv1.ResultEvent{Success: true}},
	})
}

func (s *deployFakeProviderServer) RemoveDomain(ctx context.Context, req *deploymentsv1.RemoveDomainRequest, stream *connect.ServerStream[deploymentsv1.DeployEvent]) error {
	if err := s.checkToken(ctx); err != nil {
		return err
	}
	say := func(message string) error {
		return stream.Send(&deploymentsv1.DeployEvent{
			Event: &deploymentsv1.DeployEvent_Progress{Progress: &deploymentsv1.ProgressEvent{Message: message}},
		})
	}
	if err := say(fmt.Sprintf("DOMAIN RM slug=%s host=%s configured=%s dns=%s edge=%s", req.GetSlug(), req.GetHost(), strings.Join(req.GetConfigured(), ","), req.GetEdge().GetDns().GetKind(), resolvedEdgeKind(req.GetEdge().GetKind()))); err != nil {
		return err
	}
	for _, host := range fakeDomainTargets(nil, req.GetHost()) {
		if err := say(fmt.Sprintf("Unbinding %s from the cloudflare edge", host)); err != nil {
			return err
		}
	}
	return stream.Send(&deploymentsv1.DeployEvent{
		Event: &deploymentsv1.DeployEvent_Result{Result: &deploymentsv1.ResultEvent{Success: true}},
	})
}

func fakeDomainTargets(configured []string, host string) []string {
	if host != "" {
		return []string{host}
	}
	return configured
}

const (
	fakeDomainReadyAfterEnvVar = "OCEL_TEST_FAKE_DOMAIN_READY_AFTER"
	fakeDomainCertEnvVar       = "OCEL_TEST_FAKE_DOMAIN_CERT"
	fakeDomainRenewalEnvVar    = "OCEL_TEST_FAKE_DOMAIN_RENEWAL"
	fakeDomainExpiresEnvVar    = "OCEL_TEST_FAKE_DOMAIN_EXPIRES"
	fakeDomainFailUntilEnvVar  = "OCEL_TEST_FAKE_DOMAIN_FAIL_UNTIL"
)

func (s *deployFakeProviderServer) DomainStatus(ctx context.Context, req *deploymentsv1.DomainStatusRequest) (*deploymentsv1.DomainStatusResponse, error) {
	if err := s.checkToken(ctx); err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.domainStatusCalls++
	call := s.domainStatusCalls
	s.mu.Unlock()

	if failUntil, _ := strconv.Atoi(os.Getenv(fakeDomainFailUntilEnvVar)); call > 1 && call <= failUntil {
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("the provider is briefly unreachable"))
	}
	readyAfter, _ := strconv.Atoi(os.Getenv(fakeDomainReadyAfterEnvVar))
	ready := call > readyAfter
	status, certID, _ := strings.Cut(os.Getenv(fakeDomainCertEnvVar), " ")
	expires, _ := strconv.ParseInt(os.Getenv(fakeDomainExpiresEnvVar), 10, 64)

	resp := &deploymentsv1.DomainStatusResponse{Ready: ready && len(req.GetConfigured()) > 0}
	for _, host := range req.GetConfigured() {
		row := &deploymentsv1.DomainHost{
			Hostname:          host,
			Declared:          true,
			CertificateId:     certID,
			CertificateStatus: status,
			RenewalStatus:     os.Getenv(fakeDomainRenewalEnvVar),
			ExpiresAt:         expires,
			ExpiringSoon:      expires != 0 && os.Getenv(fakeDomainRenewalEnvVar) != "SUCCESS",
			RecordsWritten:    []string{host + " AAAA 100::"},
			RecordsOwed:       splitList(os.Getenv(fakeGlobalDomainOwedEnvVar)),
			LastProbeAt:       1755500000,
			LastProbeOk:       ready,
			LastProbeEdge:     "cloudflare",
			ServingPointer:    "cloudflare",
			Ready:             ready,
		}
		if !ready {
			row.Pending = fmt.Sprintf("%s does not answer as the %s edge yet", host, edge.Kind("cloudflare"))
		}
		resp.Hosts = append(resp.Hosts, row)
	}
	return resp, nil
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

func (s *deployFakeProviderServer) PlanDestroyProject(ctx context.Context, req *deploymentsv1.PlanDestroyProjectRequest) (*deploymentsv1.PlanDestroyProjectResponse, error) {
	if err := s.checkToken(ctx); err != nil {
		return nil, err
	}
	journalEdge(req.GetEdge().GetKind(), nil, nil)
	slug := req.GetSlug()
	if req.GetEnvironment().GetClass() == deploymentsv1.Environment_CLASS_PREVIEW {
		return &deploymentsv1.PlanDestroyProjectResponse{
			EdgeStack: &deploymentsv1.EdgeStackPlan{
				EdgeKind: resolvedEdgeKind(req.GetEdge().GetKind()),
				Items: []*deploymentsv1.TeardownItem{
					{
						Kind:   "edge workers",
						Name:   slug,
						Action: deploymentsv1.TeardownItem_ACTION_DELETE,
					},
					{
						Kind:   "preview wildcard",
						Name:   "*.preview.acme.com",
						Action: deploymentsv1.TeardownItem_ACTION_KEEP,
						Reason: "substrate-scoped: every project's previews are served on it",
					},
				},
			},
			InfraStacks: []string{slug + "--pr-1--infra", slug + "--pr-2--infra"},
			AppStacks:   []string{slug + "--pr-1--web--b1"},
		}, nil
	}
	return &deploymentsv1.PlanDestroyProjectResponse{
		EdgeStack: &deploymentsv1.EdgeStackPlan{
			EdgeKind: resolvedEdgeKind(req.GetEdge().GetKind()),
			Items: []*deploymentsv1.TeardownItem{
				{
					Kind:   "edge stack",
					Name:   slug,
					Action: deploymentsv1.TeardownItem_ACTION_DELETE,
				},
				{
					Kind:   "distribution",
					Name:   "E1" + slug,
					Action: deploymentsv1.TeardownItem_ACTION_DISABLE_THEN_DELETE,
					Slow:   true,
				},
				{
					Kind:   "certificate",
					Name:   slug + ".example.com",
					Action: deploymentsv1.TeardownItem_ACTION_KEEP,
					Reason: "you pinned this certificate; Ocel never deletes one it did not request",
				},
			},
		},
		InfraStacks: []string{slug + "--infra"},
		AppStacks:   []string{slug + "--web--b1"},
	}, nil
}

func (s *deployFakeProviderServer) DestroyProject(ctx context.Context, req *deploymentsv1.DestroyProjectRequest, stream *connect.ServerStream[deploymentsv1.DeployEvent]) error {
	if err := s.checkToken(ctx); err != nil {
		return err
	}
	journalEdge(req.GetEdge().GetKind(), nil, nil)
	if err := stream.Send(&deploymentsv1.DeployEvent{
		Event: &deploymentsv1.DeployEvent_Progress{Progress: &deploymentsv1.ProgressEvent{Message: "DESTROY PROJECT project=" + req.GetSlug() + " dns=" + req.GetEdge().GetDns().GetKind() + " " + describeEnv(req.GetEnvironment())}},
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
	return fmt.Sprintf("class=%s lifecycle=%s identity=%s",
		env.GetClass(), env.GetLifecycle(), env.GetIdentity())
}

func describeFunction(f *deploymentsv1.ManifestFunction) string {
	return fmt.Sprintf("logical_name=%s runtime=%s handler=%s artifact_path=%s framework=%s app=%s",
		f.GetLogicalName(), f.GetRuntime(), f.GetHandler(), f.GetArtifactPath(), f.GetFramework(), f.GetApp())
}

func describeUsage(u *deploymentsv1.ManifestUsage) string {
	return fmt.Sprintf("app=%s resource=%s files=%s", u.GetApp(), u.GetResource(), strings.Join(u.GetFiles(), ","))
}

func describeDelivery(m *deploymentsv1.Manifest, app string) string {
	used := map[string]bool{}
	for _, u := range m.GetUsages() {
		if u.GetApp() == app {
			used[u.GetResource()] = true
		}
	}
	var delivered []string
	for _, r := range m.GetResources() {
		if used[r.GetLogicalName()] {
			delivered = append(delivered, r.GetLogicalName())
		}
	}
	slices.Sort(delivered)
	return fmt.Sprintf("app=%s resources=%s", app, strings.Join(delivered, ","))
}

func describeApp(a *deploymentsv1.ManifestApp) string {
	keys := make([]string, 0, len(a.GetVariables()))
	for _, v := range a.GetVariables() {
		keys = append(keys, v.GetKey())
	}
	return fmt.Sprintf("name=%s framework=%s production_domain=%s vars=%s deployment=%s",
		a.GetName(), a.GetFramework(), strings.Join(a.GetDomains()["production"].GetHostnames(), ","), strings.Join(keys, ","), a.GetDeploymentId())
}

func parseInfraClass(s string) deploymentsv1.Environment_Class {
	switch s {
	case "preview":
		return deploymentsv1.Environment_CLASS_PREVIEW
	case "production":
		return deploymentsv1.Environment_CLASS_PRODUCTION
	default:
		return deploymentsv1.Environment_CLASS_UNSPECIFIED
	}
}

func validateFixtureManifest(m *deploymentsv1.Manifest) error {
	if m.GetSchemaVersion() == "" {
		return errors.New("manifest missing schema_version")
	}
	for _, a := range m.GetApps() {
		if err := naming.ValidateDeploymentID(a.GetDeploymentId()); err != nil {
			return fmt.Errorf("app %s: %w", a.GetName(), err)
		}
	}
	declared := map[string]bool{}
	for _, r := range m.GetResources() {
		if r.GetLogicalName() == "" {
			return fmt.Errorf("resource %s carries no logical name", r.GetResource().GetType())
		}
		if _, ok := naming.KindOf(r.GetResource().GetType()); !ok {
			return fmt.Errorf("resource %s has type %v, which names no resource kind", r.GetLogicalName(), r.GetResource().GetType())
		}
		if r.GetResource().GetType() == linksv1.LinkType_LINK_TYPE_POSTGRES && r.GetPostgres().GetVersion() != "17" {
			return fmt.Errorf("resource %s postgres version = %q, want %q", r.GetLogicalName(), r.GetPostgres().GetVersion(), "17")
		}
		declared[r.GetLogicalName()] = true
	}
	for _, u := range m.GetUsages() {
		if !declared[u.GetResource()] {
			return fmt.Errorf("usage %s names a resource this manifest never declares", describeUsage(u))
		}
		if len(u.GetFiles()) == 0 {
			return fmt.Errorf("usage %s carries no file provenance", describeUsage(u))
		}
	}
	return nil
}
