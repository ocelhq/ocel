package clitest

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/ocelhq/ocel/cli/internal/version"
	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/localrpc"
	"github.com/ocelhq/ocel/pkg/naming"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	planv1 "github.com/ocelhq/ocel/pkg/proto/common/plan/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	"github.com/ocelhq/ocel/pkg/proto/provider/envvars/v1/envvarsv1connect"
	"github.com/ocelhq/ocel/pkg/provider"
)

const FakeProviderEnvVar = "OCEL_TEST_DEPLOY_FAKE_PROVIDER"

const fakeProviderSockEnvVar = "OCEL_TEST_DEPLOY_FAKE_PROVIDER_SOCK"

const FakeProviderModeEnvVar = "OCEL_TEST_DEPLOY_FAKE_PROVIDER_MODE"

const (
	FakeInfraTierEnvVar    = "OCEL_TEST_FAKE_INFRA_TIER"
	FakeInfraPresentEnvVar = "OCEL_TEST_FAKE_INFRA_PRESENT"
)

const (
	FakeIDProviderEnvVar  = "OCEL_TEST_FAKE_ID_PROVIDER"
	FakeIDAccountEnvVar   = "OCEL_TEST_FAKE_ID_ACCOUNT"
	FakeIDProfileEnvVar   = "OCEL_TEST_FAKE_ID_PROFILE"
	FakeIDLocationEnvVar  = "OCEL_TEST_FAKE_ID_LOCATION"
	FakeIDEdgeScopeEnvVar = "OCEL_TEST_FAKE_ID_EDGE_SCOPE"
	FakeCredProblemEnvVar = "OCEL_TEST_FAKE_CRED_PROBLEM"
)

const FakePropagationEnvVar = "OCEL_TEST_FAKE_PROPAGATION"

const FakeRollbackWarningEnvVar = "OCEL_TEST_FAKE_ROLLBACK_WARNING"

const FakeKnownSlugsEnvVar = "OCEL_TEST_FAKE_KNOWN_SLUGS"

const FakeComputesEnvVar = "OCEL_TEST_FAKE_COMPUTES"

const FakeContainerArchEnvVar = "OCEL_TEST_FAKE_CONTAINER_ARCH"

const FakePublishedBindingsEnvVar = "OCEL_TEST_FAKE_PUBLISHED_BINDINGS"

const FakeDeployJournalEnvVar = "OCEL_TEST_FAKE_DEPLOY_JOURNAL"

func journalDeployRequest(req *contractv1.DeployRequest) error {
	path := os.Getenv(FakeDeployJournalEnvVar)
	if path == "" {
		return nil
	}
	encoded, err := protojson.Marshal(req)
	if err != nil {
		return err
	}
	return os.WriteFile(path, encoded, 0o600)
}

const FakePreflightJournalEnvVar = "OCEL_TEST_FAKE_PREFLIGHT_JOURNAL"

const FakeHostnameJournalEnvVar = "OCEL_TEST_FAKE_HOSTNAME_JOURNAL"

const FakeEdgeJournalEnvVar = "OCEL_TEST_FAKE_EDGE_JOURNAL"

const FakeConfigureJournalEnvVar = "OCEL_TEST_FAKE_CONFIGURE_JOURNAL"

const FakeEnabledFeaturesEnvVar = "OCEL_TEST_FAKE_ENABLED_FEATURES"

const (
	FakeCatalogueEnvVar = "OCEL_TEST_FAKE_CATALOGUE"
	FakeCatalogueNone   = "none"
)

const FakeBootstrapEnvVar = "OCEL_TEST_FAKE_BOOTSTRAP"

const FakeEdgeRefusalEnvVar = "OCEL_TEST_FAKE_EDGE_REFUSAL"

const FakeNeedsRefusalEnvVar = "OCEL_TEST_FAKE_NEEDS_REFUSAL"

const FakeDegradedEnvVar = "OCEL_TEST_FAKE_DEGRADED"

const FakeDomainOwnerEnvVar = "OCEL_TEST_FAKE_DOMAIN_OWNER"

const (
	FakeGlobalDomainEnvVar              = "OCEL_TEST_FAKE_GLOBAL_DOMAIN"
	FakeGlobalDomainEdgeScopeEnvVar     = "OCEL_TEST_FAKE_GLOBAL_DOMAIN_EDGE_SCOPE"
	fakeGlobalDomainRouteEnvVar         = "OCEL_TEST_FAKE_GLOBAL_DOMAIN_ROUTE"
	fakeGlobalDomainGrammarEnvVar       = "OCEL_TEST_FAKE_GLOBAL_DOMAIN_GRAMMAR"
	FakeGlobalDomainProjectsEnvVar      = "OCEL_TEST_FAKE_GLOBAL_DOMAIN_PROJECTS"
	FakeGlobalDomainCertEnvVar          = "OCEL_TEST_FAKE_GLOBAL_DOMAIN_CERT"
	FakeGlobalDomainRenewalEnvVar       = "OCEL_TEST_FAKE_GLOBAL_DOMAIN_RENEWAL"
	FakeGlobalDomainExpiresEnvVar       = "OCEL_TEST_FAKE_GLOBAL_DOMAIN_EXPIRES"
	FakeGlobalDomainRecordsEnvVar       = "OCEL_TEST_FAKE_GLOBAL_DOMAIN_RECORDS"
	FakeGlobalDomainManualRecordsEnvVar = "OCEL_TEST_FAKE_GLOBAL_DOMAIN_MANUAL_RECORDS"
	FakeGlobalDomainProbeEnvVar         = "OCEL_TEST_FAKE_GLOBAL_DOMAIN_PROBE"
)

const (
	FakeAppURL        = "https://fake-app.example.com"
	FakePromotionID   = "prm_fake_1234"
	FakeBindingSecret = "pw-do-not-publish-9f2c"
)

func FixtureDeploymentID(app string) string {
	sum := sha256.Sum256([]byte("ocel-test-deployment/" + app))
	return hex.EncodeToString(sum[:])[:32]
}

func RunFakeProvider() int {
	sockPath := os.Getenv(fakeProviderSockEnvVar)
	if sockPath == "" {
		fmt.Fprintln(os.Stderr, "fake provider: missing socket path")
		return 1
	}
	if err := os.MkdirAll(filepath.Dir(sockPath), 0o700); err != nil {
		fmt.Fprintln(os.Stderr, "fake provider: reserve socket dir:", err)
		return 1
	}
	_ = os.Remove(sockPath)

	bound, err := net.Listen("unix", sockPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "fake provider: listen:", err)
		return 1
	}
	defer bound.Close()

	ln, identity, err := localrpc.SecureListener(bound)
	if err != nil {
		fmt.Fprintln(os.Stderr, "fake provider:", err)
		return 1
	}
	defer ln.Close()

	fake := &deployFakeProviderServer{mode: os.Getenv(FakeProviderModeEnvVar)}

	fmt.Println(localrpc.FormatReadinessLine(version.Version, localrpc.FormatUnixAddress(sockPath), identity.CertificateDER()))

	srv := &http.Server{Handler: fakeProviderRoutes(fake)}
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return 1
	}
	return 0
}

func fakeProviderRoutes(fake *deployFakeProviderServer) *http.ServeMux {
	mux := http.NewServeMux()
	path, handler := contractv1connect.NewProviderServiceHandler(fake, connect.WithInterceptors(endedScopes{}))
	mux.Handle(path, handler)

	path, handler = envvarsv1connect.NewEnvVarsServiceHandler(fake)
	mux.Handle(path, handler)
	return mux
}

type deployFakeProviderServer struct {
	contractv1connect.UnimplementedProviderServiceHandler
	mode string

	mu                sync.Mutex
	domainStatusCalls int
	preflightSlug     string
	preflightDomains  []string
	preflightTier     environmentv1.Tier
}

var fakeUnitID = naming.UnitID(naming.UnitEnvironment)

func declareFakeStages(stream *connect.ServerStream[progressv1.OperationEvent]) error {
	return stream.Send(&progressv1.OperationEvent{
		Level:   progressv1.Level_LEVEL_INFO,
		Phase:   progressv1.Phase_PHASE_PROVISION,
		SpanId:  fakeUnitID,
		Subject: "production",
		Message: "Applying the fake provider's changes",
		Body:    &progressv1.OperationEvent_Started{Started: &progressv1.Started{}},
	})
}

type endedScopes struct{}

func (endedScopes) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc { return next }

func (endedScopes) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (endedScopes) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		scoped := &scopedConn{StreamingHandlerConn: conn}
		err := next(ctx, scoped)
		failure := ""
		if err != nil {
			failure = err.Error()
		}
		if endErr := scoped.endOpen(err == nil, failure); err == nil {
			return endErr
		}
		return err
	}
}

type scopedConn struct {
	connect.StreamingHandlerConn
	open []*progressv1.OperationEvent
}

func (c *scopedConn) Send(msg any) error {
	if ev, ok := msg.(*progressv1.OperationEvent); ok {
		switch {
		case ev.GetStarted() != nil:
			c.open = append(c.open, &progressv1.OperationEvent{Time: timestamppb.Now(), Phase: ev.GetPhase(), SpanId: ev.GetSpanId()})
		case ev.GetEnded() != nil:
			c.open = slices.DeleteFunc(c.open, func(open *progressv1.OperationEvent) bool { return bytes.Equal(open.GetSpanId(), ev.GetSpanId()) })
		case ev.GetResult() != nil:
			if err := c.endOpen(ev.GetResult().GetSuccess(), ev.GetResult().GetError()); err != nil {
				return err
			}
		}
	}
	return c.StreamingHandlerConn.Send(msg)
}

func (c *scopedConn) endOpen(succeeded bool, failure string) error {
	level, status := progressv1.Level_LEVEL_INFO, progressv1.SpanStatus_SPAN_STATUS_OK
	if !succeeded {
		level, status = progressv1.Level_LEVEL_ERROR, progressv1.SpanStatus_SPAN_STATUS_ERROR
	}
	if innermost := len(c.open) - 1; !succeeded && failure != "" && innermost >= 0 {
		if err := c.StreamingHandlerConn.Send(&progressv1.OperationEvent{
			Time:    timestamppb.Now(),
			Level:   progressv1.Level_LEVEL_ERROR,
			Phase:   c.open[innermost].GetPhase(),
			SpanId:  c.open[innermost].GetSpanId(),
			Message: failure,
		}); err != nil {
			return err
		}
	}
	for len(c.open) > 0 {
		started := c.open[len(c.open)-1]
		c.open = c.open[:len(c.open)-1]
		if err := c.StreamingHandlerConn.Send(&progressv1.OperationEvent{
			Time:   timestamppb.Now(),
			Level:  level,
			Phase:  started.GetPhase(),
			SpanId: started.GetSpanId(),
			Body: &progressv1.OperationEvent_Ended{Ended: &progressv1.Ended{
				Status:            status,
				StartTimeUnixNano: started.GetTime().AsTime().UnixNano(),
			}},
		}); err != nil {
			return err
		}
	}
	return nil
}

func fakeProgress(message string) *progressv1.OperationEvent {
	return &progressv1.OperationEvent{
		Level:   progressv1.Level_LEVEL_INFO,
		Phase:   progressv1.Phase_PHASE_PROVISION,
		SpanId:  fakeUnitID,
		Message: message,
	}
}

func (s *deployFakeProviderServer) recordPreflight(slug string, domains []string, tier environmentv1.Tier) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.preflightSlug, s.preflightDomains, s.preflightTier = slug, domains, tier
}

func (s *deployFakeProviderServer) lastPreflight() (string, []string, environmentv1.Tier) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.preflightSlug, s.preflightDomains, s.preflightTier
}

func (s *deployFakeProviderServer) Deploy(ctx context.Context, req *contractv1.DeployRequest, stream *connect.ServerStream[progressv1.OperationEvent]) error {
	if err := declareFakeStages(stream); err != nil {
		return err
	}
	if err := journalDeployRequest(req); err != nil {
		return err
	}
	journalEdge(req.GetEdge().GetKind(), req.GetEdge().GetDns(), req.GetEdge().GetAllowDegraded())
	if err := refuseEdge(); err != nil {
		return err
	}

	if err := validateFixtureManifest(req.GetManifest()); err != nil {
		return stream.Send(&progressv1.OperationEvent{
			Body: &progressv1.OperationEvent_Result{Result: &progressv1.OperationResult{Success: false, Error: err.Error()}},
		})
	}

	for _, ev := range fakeDegradedEvents() {
		if err := stream.Send(ev); err != nil {
			return err
		}
	}
	if refusal := os.Getenv(FakeNeedsRefusalEnvVar); refusal != "" {
		return stream.Send(&progressv1.OperationEvent{
			Body: &progressv1.OperationEvent_Result{Result: &progressv1.OperationResult{Success: false, Error: refusal}},
		})
	}

	slug, domains, tier := s.lastPreflight()
	if err := stream.Send(fakeProgress("PREFLIGHT slug=" + slug + " domains=" + strings.Join(domains, ",") + " tier=" + tier.String())); err != nil {
		return err
	}

	deploying := "DEPLOY " + describeEnv(req.GetEnvironment())
	if err := stream.Send(fakeProgress(deploying)); err != nil {
		return err
	}

	for _, r := range req.GetManifest().GetResources() {
		if err := stream.Send(fakeProgress("RESOURCE name=" + r.GetResource().GetName())); err != nil {
			return err
		}
	}

	for _, a := range req.GetManifest().GetApps() {
		if err := stream.Send(fakeProgress("APP " + describeApp(a))); err != nil {
			return err
		}
	}

	for _, a := range req.GetManifest().GetApps() {
		for _, f := range a.GetServerless().GetFunctions() {
			if err := stream.Send(fakeProgress("FUNCTION " + describeFunction(a.GetName(), f))); err != nil {
				return err
			}
		}
	}

	for _, a := range req.GetManifest().GetApps() {
		if c := a.GetContainer(); c != nil {
			if err := stream.Send(fakeProgress("CONTAINER " + describeContainer(a.GetName(), c))); err != nil {
				return err
			}
		}
	}

	if registry := req.GetProjectRegistry(); registry.GetServer() != "" {
		if err := stream.Send(fakeProgress("REGISTRY " + describeRegistry(registry))); err != nil {
			return err
		}
	}

	for _, message := range consumeFakeBindings(req) {
		if err := stream.Send(fakeProgress(message)); err != nil {
			return err
		}
	}
	if refusal := refuseUnpublishedFakeBindings(req); refusal != "" {
		return stream.Send(&progressv1.OperationEvent{
			Body: &progressv1.OperationEvent_Result{Result: &progressv1.OperationResult{Success: false, Error: refusal}},
		})
	}

	for _, u := range req.GetManifest().GetUsages() {
		if err := stream.Send(fakeProgress("USAGE " + describeUsage(u))); err != nil {
			return err
		}
	}

	for _, a := range req.GetManifest().GetApps() {
		if err := stream.Send(fakeProgress("DELIVER " + describeDelivery(req.GetManifest(), a.GetName()))); err != nil {
			return err
		}
	}

	if req.GetDry() {
		if err := stream.Send(&progressv1.OperationEvent{
			Body: &progressv1.OperationEvent_Plan{Plan: fakeDeployPlan(req)},
		}); err != nil {
			return err
		}
		return stream.Send(&progressv1.OperationEvent{
			Body: &progressv1.OperationEvent_Result{Result: &progressv1.OperationResult{Success: true}},
		})
	}

	if err := stream.Send(fakeProgress("provisioning...")); err != nil {
		return err
	}

	if s.mode == "fail" {
		return stream.Send(&progressv1.OperationEvent{
			Body: &progressv1.OperationEvent_Result{Result: &progressv1.OperationResult{Success: false, Error: "simulated deploy failure"}},
		})
	}
	return stream.Send(&progressv1.OperationEvent{
		Body: &progressv1.OperationEvent_Result{Result: &progressv1.OperationResult{
			Success:     true,
			Apps:        fakeAppResults(req.GetManifest()),
			PromotionId: FakePromotionID,
			Propagation: fakePropagation(),
			Bindings:    fakeBindings(req.GetManifest()),
		}},
	})
}

func fakeDeployPlan(req *contractv1.DeployRequest) *planv1.ChangePlan {
	manifest := req.GetManifest()
	tier := strings.ToLower(strings.TrimPrefix(req.GetEnvironment().GetTier().String(), "TIER_"))

	infra := &planv1.ChangeGroup{Kind: "stack", Name: "fake/" + manifest.GetSlug() + "--infra", Action: planv1.Change_ACTION_CREATE}
	values := &planv1.ChangeGroup{Kind: "parameters", Name: "values", Action: planv1.Change_ACTION_CREATE}
	for _, r := range manifest.GetResources() {
		name := r.GetResource().GetName()
		infra.Changes = append(infra.Changes, &planv1.Change{Kind: "postgres", Name: name, Action: planv1.Change_ACTION_CREATE})
		values.Changes = append(values.Changes, &planv1.Change{Kind: "postgres", Name: name, Action: planv1.Change_ACTION_CREATE})
	}

	plan := &planv1.ChangePlan{Subject: manifest.GetSlug(), EdgeKind: resolvedEdgeKind(req.GetEdge().GetKind())}
	plan.Groups = append(plan.Groups, infra)
	if len(values.Changes) > 0 {
		plan.Groups = append(plan.Groups, values)
	}
	promotion := &planv1.ChangeGroup{Kind: "promotion", Name: tier, Action: planv1.Change_ACTION_CREATE}
	for _, app := range manifest.GetApps() {
		group := &planv1.ChangeGroup{
			Kind:   "stack",
			Name:   "fake/" + manifest.GetSlug() + "--" + app.GetName(),
			Action: planv1.Change_ACTION_CREATE,
		}
		for _, fn := range app.GetServerless().GetFunctions() {
			group.Changes = append(group.Changes,
				&planv1.Change{Kind: "artifact", Name: fn.GetLogicalName(), Action: planv1.Change_ACTION_CREATE},
				&planv1.Change{Kind: "function", Name: fn.GetLogicalName(), Action: planv1.Change_ACTION_CREATE})
		}
		if c := app.GetContainer(); c != nil {
			group.Changes = append(group.Changes,
				&planv1.Change{Kind: "container", Name: c.GetImage(), Action: planv1.Change_ACTION_CREATE})
		}
		plan.Groups = append(plan.Groups, group)
		promotion.Changes = append(promotion.Changes,
			&planv1.Change{Kind: "deployment", Name: app.GetName(), Action: planv1.Change_ACTION_CREATE})
	}
	plan.Groups = append(plan.Groups,
		&planv1.ChangeGroup{
			Kind:   "edge",
			Name:   resolvedEdgeKind(req.GetEdge().GetKind()) + "/edge",
			Action: planv1.Change_ACTION_CREATE,
			Reason: "reconciled to serve this release",
		},
		promotion)
	return plan
}

func fakePropagation() *progressv1.Propagation {
	spec := os.Getenv(FakePropagationEnvVar)
	if spec == "" {
		return nil
	}
	typical, published, _ := strings.Cut(spec, ":")
	ms, err := strconv.ParseInt(typical, 10, 64)
	if err != nil {
		return nil
	}
	return &progressv1.Propagation{TypicalMs: ms, Published: published == "published"}
}

func fakePublishedBindings() []string {
	var out []string
	for _, name := range strings.Split(os.Getenv(FakePublishedBindingsEnvVar), ",") {
		if name = strings.TrimSpace(name); name != "" {
			out = append(out, name)
		}
	}
	return out
}

func carriesFakeInline(req *contractv1.DeployRequest, name string) bool {
	return slices.ContainsFunc(req.GetInlineBindings(), func(b *bindingsv1.Binding) bool { return b.GetName() == name })
}

func consumeFakeBindings(req *contractv1.DeployRequest) []string {
	published := fakePublishedBindings()
	var out []string
	for _, r := range req.GetManifest().GetResources() {
		id := r.GetResource().GetName()
		if r.GetBinding() != "" {
			line := "BINDING bound=" + r.GetLogicalName() + " name=" + id
			if carriesFakeInline(req, r.GetBinding()) {
				line += " record=" + r.GetBinding() + " carried"
			} else if stored := storedFakeBinding(req.GetEnvironment(), r.GetBinding()); stored != nil {
				line += " record=" + stored.Name + " owner=" + stored.Owner
			}
			out = append(out, line)
			continue
		}
		if slices.Contains(published, id) {
			out = append(out, "BINDING shadowed="+r.GetLogicalName()+" name="+id)
		}
	}
	return out
}

func storedFakeBinding(env *environmentv1.Environment, name string) *fakeBinding {
	store, err := loadFakeBindingStore()
	if err != nil {
		return nil
	}
	for _, binding := range store {
		if binding.Tier == env.GetTier() && binding.Name == name && (binding.Environment == "" || binding.Environment == env.GetIdentity()) {
			return binding
		}
	}
	return nil
}

func refuseUnpublishedFakeBindings(req *contractv1.DeployRequest) string {
	published := fakePublishedBindings()
	var missing []string
	for _, r := range req.GetManifest().GetResources() {
		if carriesFakeInline(req, r.GetBinding()) {
			continue
		}
		if r.GetBinding() != "" && !slices.Contains(published, r.GetResource().GetName()) && storedFakeBinding(req.GetEnvironment(), r.GetBinding()) == nil {
			missing = append(missing, r.GetResource().GetName())
		}
	}
	if len(missing) == 0 {
		return ""
	}
	return "nothing has published a binding named " + strings.Join(missing, ", ") + " to production"
}

func fakeAppResults(m *contractv1.Manifest) []*progressv1.AppResult {
	out := make([]*progressv1.AppResult, 0, len(m.GetApps()))
	for _, app := range m.GetApps() {
		out = append(out, &progressv1.AppResult{
			App:     app.GetName(),
			Outcome: progressv1.AppOutcome_APP_OUTCOME_SUCCEEDED,
			Urls:    []string{FakeAppURL},
		})
	}
	return out
}

func fakeBindings(m *contractv1.Manifest) []*bindingsv1.Binding {
	out := make([]*bindingsv1.Binding, 0, len(m.GetResources()))
	for _, r := range m.GetResources() {
		if r.GetBinding() != "" {
			continue
		}
		binding := &bindingsv1.Binding{
			Name: r.GetLogicalName(),
			Grants: []*bindingsv1.Grant{{
				Actions:   []string{"fake:connect"},
				Resources: []string{"fake:resource/main"},
				Label:     "connect",
			}},
		}
		switch r.GetResource().GetType() {
		case resourcesv1.ResourceType_RESOURCE_TYPE_BUCKET:
			binding.Properties = &bindingsv1.Binding_Bucket{Bucket: &bindingsv1.BucketProperties{Bucket: r.GetResource().GetName() + "-" + FakeBindingSecret}}
		default:
			binding.Properties = &bindingsv1.Binding_Postgres{Postgres: &bindingsv1.PostgresProperties{
				Host:     "db.fake.internal",
				Port:     5432,
				Username: "app",
				Password: FakeBindingSecret,
				Database: r.GetResource().GetName(),
			}}
		}
		out = append(out, binding)
	}
	return out
}

func (s *deployFakeProviderServer) DescribeBootstrap(ctx context.Context, req *contractv1.DescribeBootstrapRequest) (*contractv1.DescribeBootstrapResponse, error) {
	enabled := strings.Split(os.Getenv(FakeEnabledFeaturesEnvVar), ",")
	feature := func(name, summary string, dependsOn ...string) *contractv1.Feature {
		return &contractv1.Feature{
			Name:      name,
			Summary:   summary,
			DependsOn: dependsOn,
			Enabled:   slices.Contains(enabled, name),
		}
	}
	fronting := func(name, summary, kind string, dependsOn ...string) *contractv1.Feature {
		f := feature(name, summary, dependsOn...)
		f.Edges = []string{kind}
		return f
	}
	var catalogue []*contractv1.Feature
	if os.Getenv(FakeCatalogueEnvVar) != FakeCatalogueNone {
		catalogue = []*contractv1.Feature{
			feature("isr", "incremental static regeneration"),
			feature("image-optimization", "on-demand image optimization"),
			feature(provider.FeatureVarsKey, "a key the variables are sealed under"),
			fronting("relay-edge", "a relay front", "relay", "isr"),
			fronting("direct-edge", "a direct front", "direct"),
		}
	}
	return &contractv1.DescribeBootstrapResponse{
		Features:  catalogue,
		Bootstrap: fakeBootstrap(req.GetTier()),
	}, nil
}

func fakeChangePlan(req *contractv1.BootstrapRequest) *planv1.ChangePlan {
	tier := strings.ToLower(strings.TrimPrefix(req.GetTier().String(), "TIER_"))
	return &planv1.ChangePlan{
		Subject:  tier,
		EdgeKind: resolvedEdgeKind(req.GetEdge().GetKind()),
		Groups:   []*planv1.ChangeGroup{{Kind: "stack", Name: "fake/ocel-" + tier + "-core", Action: planv1.Change_ACTION_CREATE}},
	}
}

func fakeBootstrap(tier environmentv1.Tier) *contractv1.BootstrapStatus {
	var shape string
	if tier != environmentv1.Tier_TIER_PREVIEW {
		shape = os.Getenv(FakeBootstrapEnvVar)
	}
	if shape == "" {
		return &contractv1.BootstrapStatus{Tier: tier, RequiredSchema: 1, Writer: "1.4.0"}
	}
	status := &contractv1.BootstrapStatus{
		Tier:           tier,
		Present:        true,
		Schema:         1,
		RequiredSchema: 1,
		Writer:         "1.4.0",
		Stacks: []*contractv1.BootstrapStack{
			{Name: "ocel-bootstrap", Present: true, Schema: 1, DigestCurrent: true, WrittenBy: "1.4.0", Required: true},
			{Name: "ocel-bootstrap-isr", Feature: "isr", Present: true, Schema: 1, DigestCurrent: true, WrittenBy: "1.4.0", Required: true},
			{Name: "ocel-bootstrap-image-optimization", Feature: "image-optimization"},
			{Name: "ocel-bootstrap-vars-key", Feature: "vars-key"},
		},
	}
	switch shape {
	case "stale":
		status.Stacks[1].DigestCurrent = false
	case "stale-optional":
		status.Stacks[2].Present = true
	case "unfinished":
		status.Unfinished = true
	case "missing":
		status.Stacks[2].Required = true
	case "ahead":
		status.Schema = 2
		status.Stacks[0].Schema = 2
		status.Stacks[1].Schema = 2
	case "vars-key":
		status.Stacks[3].Present = true
	case "downgrade":
		status.Downgrade = true
		status.Stacks[0].WrittenBy = "1.9.0"
	}
	return status
}

func (s *deployFakeProviderServer) Bootstrap(ctx context.Context, req *contractv1.BootstrapRequest, stream *connect.ServerStream[progressv1.OperationEvent]) error {
	if plan := fakeChangePlan(req); plan != nil && req.GetConsented() == nil {
		if err := stream.Send(&progressv1.OperationEvent{Body: &progressv1.OperationEvent_Plan{Plan: plan}}); err != nil {
			return err
		}
	}
	if req.GetDry() {
		return stream.Send(&progressv1.OperationEvent{
			Body: &progressv1.OperationEvent_Result{Result: &progressv1.OperationResult{Success: true}},
		})
	}
	journalBootstrap(req)
	return stream.Send(&progressv1.OperationEvent{
		Body: &progressv1.OperationEvent_Result{Result: &progressv1.OperationResult{Success: true}},
	})
}

func (s *deployFakeProviderServer) PlanRemoveBootstrap(ctx context.Context, req *contractv1.BootstrapScope) (*planv1.ChangePlan, error) {
	journalEdge(req.GetEdge().GetKind(), nil, nil)
	if err := refuseEdge(); err != nil {
		return nil, err
	}
	tier := strings.ToLower(strings.TrimPrefix(req.GetTier().String(), "TIER_"))
	if os.Getenv(FakeEmptyRemovalPlanEnvVar) != "" {
		return &planv1.ChangePlan{Subject: tier}, nil
	}
	return &planv1.ChangePlan{
		EdgeKind: "relay",
		Subject:  tier,
		Groups: []*planv1.ChangeGroup{
			{
				Kind:    "stack",
				Name:    "fake/ocel-" + tier + "-isr",
				Feature: "isr",
				Action:  planv1.Change_ACTION_DELETE,
				Changes: []*planv1.Change{
					{Kind: "Fake::Table", Name: "RevalidationTable", Action: planv1.Change_ACTION_DELETE},
				},
			},
			{
				Kind:   "stack",
				Name:   "fake/ocel-" + tier,
				Action: planv1.Change_ACTION_DELETE,
				Changes: []*planv1.Change{
					{Kind: "Fake::Table", Name: "StateTable", Action: planv1.Change_ACTION_DELETE},
					{
						Kind:   "Fake::Bucket",
						Name:   "StateBucket",
						Action: planv1.Change_ACTION_DELETE,
						Reason: "the Pulumi state of every stack this bootstrap deployed",
						Slow:   true,
					},
				},
			},
			{
				Kind:   "parameters",
				Name:   "fake/parameters",
				Action: planv1.Change_ACTION_DELETE,
				Changes: []*planv1.Change{
					{Kind: "Fake::Parameter", Name: "/ocel/origin/secret", Action: planv1.Change_ACTION_DELETE},
					{
						Kind:   "Fake::Parameter",
						Name:   "/ocel/pulumi/passphrase",
						Action: planv1.Change_ACTION_KEEP,
						Reason: "the production bootstrap is still provisioned and its Pulumi state is encrypted under it",
					},
				},
			},
			{
				Kind:    "edge",
				Name:    "relay/edge",
				Feature: "relay-edge",
				Action:  planv1.Change_ACTION_DELETE,
				Changes: []*planv1.Change{
					{Kind: "Fake::EdgeScript", Name: "ocel-deployments-store", Action: planv1.Change_ACTION_DELETE},
				},
			},
		},
	}, nil
}

func (s *deployFakeProviderServer) RemoveBootstrap(ctx context.Context, req *contractv1.BootstrapScope, stream *connect.ServerStream[progressv1.OperationEvent]) error {
	if err := declareFakeStages(stream); err != nil {
		return err
	}
	journalEdge(req.GetEdge().GetKind(), nil, nil)
	if err := refuseEdge(); err != nil {
		return err
	}
	if err := stream.Send(fakeProgress("TEARDOWN tier=" + req.GetTier().String())); err != nil {
		return err
	}
	return stream.Send(&progressv1.OperationEvent{
		Body: &progressv1.OperationEvent_Result{Result: &progressv1.OperationResult{Success: true}},
	})
}

func fakeContainerArchs(containers []*contractv1.ContainerApp) map[string]string {
	runs := os.Getenv(FakeContainerArchEnvVar)
	if runs == "" || len(containers) == 0 {
		return nil
	}
	archs := make(map[string]string, len(containers))
	for _, container := range containers {
		archs[container.GetApp()] = runs
	}
	return archs
}

func (s *deployFakeProviderServer) Preflight(ctx context.Context, req *contractv1.PreflightRequest) (*contractv1.PreflightResponse, error) {
	s.recordPreflight(req.GetSlug(), req.GetDomains(), req.GetRequiredTier())
	journalPreflight(req)
	resp := &contractv1.PreflightResponse{
		Computes:              fakeComputes(),
		ContainerArchs:        fakeContainerArchs(req.GetContainers()),
		InfraTier:             parseInfraTier(os.Getenv(FakeInfraTierEnvVar)),
		InfrastructurePresent: os.Getenv(FakeInfraPresentEnvVar) != "0",
		Identity: &contractv1.Identity{
			Provider:  os.Getenv(FakeIDProviderEnvVar),
			Account:   os.Getenv(FakeIDAccountEnvVar),
			EdgeScope: os.Getenv(FakeIDEdgeScopeEnvVar),
			Location:  os.Getenv(FakeIDLocationEnvVar),
			Details: []*contractv1.Detail{
				{Label: "profile", Value: os.Getenv(FakeIDProfileEnvVar)},
			},
		},
	}
	if req.GetSlug() != "" && resp.InfrastructurePresent {
		for _, s := range strings.Split(os.Getenv(FakeKnownSlugsEnvVar), ",") {
			if s = strings.TrimSpace(s); s != "" {
				resp.KnownSlugs = append(resp.KnownSlugs, s)
			}
		}
	}
	owner := os.Getenv(FakeDomainOwnerEnvVar)
	for _, host := range req.GetDomains() {
		claim := &contractv1.DomainClaim{Hostname: host, Status: contractv1.DomainClaim_STATUS_UNCLAIMED}
		if owner != "" {
			claim.Status, claim.Owner = contractv1.DomainClaim_STATUS_CLAIMED, owner
		}
		resp.DomainClaims = append(resp.DomainClaims, claim)
	}
	resp.Bootstrap = fakeBootstrap(req.GetRequiredTier())
	resp.PreviewWildcard = fakeGlobalDomain()
	if p := os.Getenv(FakeCredProblemEnvVar); p != "" {
		resp.CredentialProblems = append(resp.CredentialProblems, &contractv1.CredentialProblem{
			Provider: p,
			Message:  "could not authenticate",
			Hint:     "configure the credential and re-run",
		})
	}
	return resp, nil
}

func journalHostnames(req *contractv1.HostnameRequest) {
	path := os.Getenv(FakeHostnameJournalEnvVar)
	if path == "" {
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		fmt.Fprintln(os.Stderr, "fake provider: hostname journal:", err)
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "slug=%s configured=%s probe=%v\n", req.GetSlug(), strings.Join(journalledHosts(req.GetConfigured()), ","), req.GetProbe())
}

func journalledHosts(configured []*contractv1.ConfiguredHostname) []string {
	named := make([]string, 0, len(configured))
	for _, host := range configured {
		if host.GetApp() == "" {
			named = append(named, host.GetHostname())
			continue
		}
		named = append(named, host.GetHostname()+"@"+host.GetApp())
	}
	return named
}

func journalPreflight(req *contractv1.PreflightRequest) {
	path := os.Getenv(FakePreflightJournalEnvVar)
	if path == "" {
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		fmt.Fprintln(os.Stderr, "fake provider: preflight journal:", err)
		return
	}
	defer f.Close()
	containers := make([]string, 0, len(req.GetContainers()))
	for _, container := range req.GetContainers() {
		containers = append(containers, container.GetApp())
	}
	fmt.Fprintf(f, "slug=%s domains=%s tier=%s containers=%s\n", req.GetSlug(), strings.Join(req.GetDomains(), ","), req.GetRequiredTier(), strings.Join(containers, ","))
}

func resolvedEdgeKind(kind string) string {
	if kind == "" {
		return "direct"
	}
	return kind
}

func edgeRowKind(kind string) string {
	if resolvedEdgeKind(kind) == "relay" {
		return "Fake::EdgeScript"
	}
	return "Fake::Front"
}

func journalEdge(kind string, dns *contractv1.Dns, allowDegraded []string) {
	path := os.Getenv(FakeEdgeJournalEnvVar)
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

func (s *deployFakeProviderServer) Configure(ctx context.Context, req *contractv1.ConfigureRequest) (*contractv1.ConfigureResponse, error) {
	options, err := decodeFakeProviderOptions(req.GetConfig())
	if err != nil {
		return nil, provider.RefusalError(err)
	}
	path := os.Getenv(FakeConfigureJournalEnvVar)
	if path == "" {
		return &contractv1.ConfigureResponse{}, nil
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fmt.Fprintf(f, "location=%s transforms=%s certificates=%v\n", options.Location, strings.Join(req.GetConfig().GetTransforms(), ","), options.Certificates)
	return &contractv1.ConfigureResponse{}, nil
}

type fakeProviderOptions struct {
	Location     string            `json:"location"`
	Certificates map[string]string `json:"certificates"`
	SSH          json.RawMessage   `json:"ssh"`
}

func decodeFakeProviderOptions(config *contractv1.ProviderConfig) (fakeProviderOptions, error) {
	return provider.Decode[fakeProviderOptions]("fake", provider.Options(config.GetOptions().AsMap()))
}

func journalBootstrap(req *contractv1.BootstrapRequest) {
	path := os.Getenv(FakeEdgeJournalEnvVar)
	if path == "" {
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		fmt.Fprintln(os.Stderr, "fake provider: edge journal:", err)
		return
	}
	defer f.Close()
	line := "features=" + strings.Join(req.GetFeatures(), ",")
	if removing := req.GetRemove(); len(removing) > 0 {
		line += " remove=" + strings.Join(removing, ",")
	}
	line += fmt.Sprintf(" force=%t acceptReplacements=%t", req.GetForce(), req.GetAcceptReplacements())
	if req.RepairOnDeploy != nil {
		line += fmt.Sprintf(" repairOnDeploy=%t", req.GetRepairOnDeploy())
	}
	fmt.Fprintln(f, line)
}

func fakeDegradedEvents() []*progressv1.OperationEvent {
	var events []*progressv1.OperationEvent
	for _, entry := range strings.Split(os.Getenv(FakeDegradedEnvVar), ";") {
		need, detail, ok := strings.Cut(entry, "=")
		if !ok || need == "" {
			continue
		}
		events = append(events, &progressv1.OperationEvent{
			Level:   progressv1.Level_LEVEL_WARN,
			Phase:   progressv1.Phase_PHASE_CHECK,
			Message: need + ": " + detail,
		})
	}
	return events
}

func refuseEdge() error {
	msg := os.Getenv(FakeEdgeRefusalEnvVar)
	if msg == "" {
		return nil
	}
	return connect.NewError(connect.CodeInvalidArgument, errors.New(msg))
}

func fakeGlobalDomain() *contractv1.PreviewWildcard {
	base := os.Getenv(FakeGlobalDomainEnvVar)
	if base == "" {
		return nil
	}
	grammarMin, grammarMax := uint32(1), uint32(1)
	if g := os.Getenv(fakeGlobalDomainGrammarEnvVar); g != "" {
		lo, hi, _ := strings.Cut(g, "-")
		grammarMin, grammarMax = parseGrammar(lo), parseGrammar(hi)
	}
	status, certID, _ := strings.Cut(os.Getenv(FakeGlobalDomainCertEnvVar), " ")
	probeAt, probeOK := fakeGlobalDomainProbe()
	expires, _ := strconv.ParseInt(os.Getenv(FakeGlobalDomainExpiresEnvVar), 10, 64)
	return &contractv1.PreviewWildcard{
		RenewalStatus:  os.Getenv(FakeGlobalDomainRenewalEnvVar),
		ExpiresAt:      expires,
		ExpiringSoon:   expires != 0 && time.Until(time.Unix(expires, 0)) < 30*24*time.Hour,
		BaseDomain:     base,
		EdgeScope:      os.Getenv(FakeGlobalDomainEdgeScopeEnvVar),
		GrammarMin:     grammarMin,
		GrammarMax:     grammarMax,
		RouteInstalled: os.Getenv(fakeGlobalDomainRouteEnvVar) != "0",
		Certificate: &contractv1.CertificateState{
			CertificateId:     certID,
			CertificateStatus: status,
			RecordsWritten:    splitList(os.Getenv(FakeGlobalDomainRecordsEnvVar)),
			ManualRecords:     splitList(os.Getenv(FakeGlobalDomainManualRecordsEnvVar)),
			LastProbeAt:       probeAt,
			LastProbeOk:       probeOK,
		},
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

func fakeGlobalDomainProbe() (int64, bool) {
	fields := strings.Fields(os.Getenv(FakeGlobalDomainProbeEnvVar))
	if len(fields) == 0 {
		return 0, false
	}
	at, err := strconv.ParseInt(fields[0], 10, 64)
	if err != nil {
		return 0, false
	}
	return at, len(fields) < 2 || fields[1] != "failed"
}

func parseGrammar(s string) uint32 {
	n, err := strconv.ParseUint(strings.TrimSpace(s), 10, 32)
	if err != nil {
		return 0
	}
	return uint32(n)
}

func (s *deployFakeProviderServer) UsePreviewWildcard(ctx context.Context, req *contractv1.UsePreviewWildcardRequest, stream *connect.ServerStream[progressv1.OperationEvent]) error {
	if err := declareFakeStages(stream); err != nil {
		return err
	}
	if err := stream.Send(fakeProgress("USE DOMAIN tier=" + req.GetTier().String() + " base=" + req.GetBaseDomain() + " dns=" + req.GetEdge().GetDns().GetKind())); err != nil {
		return err
	}
	records, err := edge.RecordsFor(edge.DNSTarget{Kind: "relay", ServesUnbound: true}, []string{"*." + req.GetBaseDomain()})
	if err != nil {
		return err
	}
	for _, rec := range records {
		message := "Writing " + rec.String()
		if req.GetEdge().GetDns() == nil {
			message = rec.Instruction()
		}
		if err := stream.Send(fakeProgress(message)); err != nil {
			return err
		}
	}
	return stream.Send(&progressv1.OperationEvent{
		Body: &progressv1.OperationEvent_Result{Result: &progressv1.OperationResult{Success: true}},
	})
}

const FakeServedPreviewsEnvVar = "OCEL_TEST_FAKE_SERVED_PREVIEWS"

const FakeEmptyRemovalPlanEnvVar = "OCEL_TEST_FAKE_EMPTY_REMOVAL_PLAN"

func (s *deployFakeProviderServer) PlanRemovePreviewWildcard(ctx context.Context, req *contractv1.PreviewWildcardRequest) (*planv1.ChangePlan, error) {
	base := os.Getenv(FakeGlobalDomainEnvVar)
	if base == "" {
		return &planv1.ChangePlan{}, nil
	}
	if served := os.Getenv(FakeServedPreviewsEnvVar); served != "" {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf(
			"*.%s still has live preview pointers for %s — run `ocel preview rm` or `ocel destroy preview` in each of them first",
			base, served,
		))
	}
	return &planv1.ChangePlan{
		EdgeKind: "relay",
		Subject:  base,
		Groups: []*planv1.ChangeGroup{
			{
				Kind:   "preview entry router",
				Name:   "*." + base,
				Action: planv1.Change_ACTION_DELETE,
				Reason: "the shared entry router serving this wildcard",
			},
			{
				Kind:   "DNS record",
				Name:   "*." + base + " CNAME you.example.com",
				Action: planv1.Change_ACTION_KEEP,
				Reason: "you created it yourself; ocel never wrote it",
			},
		},
	}, nil
}

func (s *deployFakeProviderServer) RemovePreviewWildcard(ctx context.Context, req *contractv1.PreviewWildcardRequest, stream *connect.ServerStream[progressv1.OperationEvent]) error {
	if err := declareFakeStages(stream); err != nil {
		return err
	}
	if err := stream.Send(fakeProgress("RELEASE DOMAIN tier=" + req.GetTier().String() + " dns=" + req.GetEdge().GetDns().GetKind())); err != nil {
		return err
	}
	return stream.Send(&progressv1.OperationEvent{
		Body: &progressv1.OperationEvent_Result{Result: &progressv1.OperationResult{Success: true}},
	})
}

const FakeDomainTimeoutEnvVar = "OCEL_TEST_FAKE_DOMAIN_TIMEOUT"

func (s *deployFakeProviderServer) AddHostname(ctx context.Context, req *contractv1.HostnameRequest, stream *connect.ServerStream[progressv1.OperationEvent]) error {
	if err := declareFakeStages(stream); err != nil {
		return err
	}
	say := func(message string) error {
		return stream.Send(fakeProgress(message))
	}
	if host := req.GetHost(); host != "" && !slices.Contains(fakeConfigured(req), host) {
		return stream.Send(&progressv1.OperationEvent{
			Body: &progressv1.OperationEvent_Result{Result: &progressv1.OperationResult{
				Success: false,
				Error:   fmt.Sprintf("this project does not declare %q: add it to domains.production and run this again — no command edits the config, which declares %s", host, strings.Join(fakeConfigured(req), ", ")),
			}},
		})
	}
	hosts := fakeDomainTargets(fakeConfigured(req), req.GetHost())
	if err := say(fmt.Sprintf("DOMAIN ADD slug=%s hosts=%s dns=%s edge=%s", req.GetSlug(), strings.Join(hosts, ","), req.GetEdge().GetDns().GetKind(), resolvedEdgeKind(req.GetEdge().GetKind()))); err != nil {
		return err
	}
	if err := say(fmt.Sprintf("Requesting a certificate for %s", strings.Join(hosts, ", "))); err != nil {
		return err
	}
	for _, host := range hosts {
		if err := say(fmt.Sprintf("Binding %s to the relay edge", host)); err != nil {
			return err
		}
		records, err := edge.RecordsFor(edge.DNSTarget{Kind: "relay", ServesUnbound: true}, []string{host})
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
		if outstanding := os.Getenv(FakeDomainTimeoutEnvVar); outstanding != "" {
			return stream.Send(&progressv1.OperationEvent{
				Body: &progressv1.OperationEvent_Result{Result: &progressv1.OperationResult{
					Success: false,
					Error:   fmt.Sprintf("gave up after 5m0s waiting for https://%s/ to answer through the relay edge; still outstanding: %s", host, outstanding),
				}},
			})
		}
		if err := say(fmt.Sprintf("%s is served through the relay edge", host)); err != nil {
			return err
		}
	}
	return stream.Send(&progressv1.OperationEvent{
		Body: &progressv1.OperationEvent_Result{Result: &progressv1.OperationResult{Success: true}},
	})
}

func (s *deployFakeProviderServer) RemoveHostname(ctx context.Context, req *contractv1.HostnameRequest, stream *connect.ServerStream[progressv1.OperationEvent]) error {
	if err := declareFakeStages(stream); err != nil {
		return err
	}
	say := func(message string) error {
		return stream.Send(fakeProgress(message))
	}
	if err := say(fmt.Sprintf("DOMAIN RM slug=%s host=%s configured=%s dns=%s edge=%s", req.GetSlug(), req.GetHost(), strings.Join(fakeConfigured(req), ","), req.GetEdge().GetDns().GetKind(), resolvedEdgeKind(req.GetEdge().GetKind()))); err != nil {
		return err
	}
	for _, host := range fakeDomainTargets(nil, req.GetHost()) {
		if err := say(fmt.Sprintf("Unbinding %s from the relay edge", host)); err != nil {
			return err
		}
	}
	return stream.Send(&progressv1.OperationEvent{
		Body: &progressv1.OperationEvent_Result{Result: &progressv1.OperationResult{Success: true}},
	})
}

func fakeConfigured(req *contractv1.HostnameRequest) []string {
	named := make([]string, 0, len(req.GetConfigured()))
	for _, host := range req.GetConfigured() {
		named = append(named, host.GetHostname())
	}
	return named
}

func fakeDomainTargets(configured []string, host string) []string {
	if host != "" {
		return []string{host}
	}
	return configured
}

const (
	FakeDomainReadyAfterEnvVar = "OCEL_TEST_FAKE_DOMAIN_READY_AFTER"
	FakeDomainCertEnvVar       = "OCEL_TEST_FAKE_DOMAIN_CERT"
	FakeDomainExpiresEnvVar    = "OCEL_TEST_FAKE_DOMAIN_EXPIRES"
	FakeDomainFailUntilEnvVar  = "OCEL_TEST_FAKE_DOMAIN_FAIL_UNTIL"
)

func (s *deployFakeProviderServer) GetHostnameStatus(ctx context.Context, req *contractv1.HostnameRequest) (*contractv1.GetHostnameStatusResponse, error) {
	journalHostnames(req)
	s.mu.Lock()
	s.domainStatusCalls++
	call := s.domainStatusCalls
	s.mu.Unlock()

	if failUntil, _ := strconv.Atoi(os.Getenv(FakeDomainFailUntilEnvVar)); call > 1 && call <= failUntil {
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("the provider is briefly unreachable"))
	}
	readyAfter, _ := strconv.Atoi(os.Getenv(FakeDomainReadyAfterEnvVar))
	ready := call > readyAfter
	status, certID, _ := strings.Cut(os.Getenv(FakeDomainCertEnvVar), " ")
	expires, _ := strconv.ParseInt(os.Getenv(FakeDomainExpiresEnvVar), 10, 64)

	resp := &contractv1.GetHostnameStatusResponse{Ready: ready && len(req.GetConfigured()) > 0, ManualRecords: splitList(os.Getenv(FakeGlobalDomainManualRecordsEnvVar))}
	for _, host := range fakeConfigured(req) {
		row := &contractv1.ProductionHostname{
			Hostname: host,
			Declared: true,
			Certificate: &contractv1.CertificateState{
				CertificateId:     certID,
				CertificateStatus: status,
				RecordsWritten:    []string{host + " AAAA 100::"},
				ManualRecords:     splitList(os.Getenv(FakeGlobalDomainManualRecordsEnvVar)),
				LastProbeAt:       1755500000,
				LastProbeOk:       ready,
			},
			ExpiresAt:      expires,
			ExpiringSoon:   expires != 0,
			ServingPointer: "relay",
			Ready:          ready,
		}
		if !ready {
			row.Pending = fmt.Sprintf("%s does not answer through the %s edge yet", host, edge.Kind("relay"))
		}
		resp.Hostnames = append(resp.Hostnames, row)
	}
	return resp, nil
}

func (s *deployFakeProviderServer) GetPreviewWildcard(ctx context.Context, req *contractv1.PreviewWildcardRequest) (*contractv1.GetPreviewWildcardResponse, error) {
	resp := &contractv1.GetPreviewWildcardResponse{Wildcard: fakeGlobalDomain()}
	for _, p := range strings.Split(os.Getenv(FakeGlobalDomainProjectsEnvVar), ",") {
		if p = strings.TrimSpace(p); p != "" {
			resp.Projects = append(resp.Projects, p)
		}
	}
	return resp, nil
}

func (s *deployFakeProviderServer) RemoveEnvironment(ctx context.Context, req *contractv1.RemoveEnvironmentRequest, stream *connect.ServerStream[progressv1.OperationEvent]) error {
	if err := declareFakeStages(stream); err != nil {
		return err
	}
	if err := stream.Send(fakeProgress("DESTROY project=" + req.GetSlug() + " " + describeEnv(req.GetEnvironment()))); err != nil {
		return err
	}
	return stream.Send(&progressv1.OperationEvent{
		Body: &progressv1.OperationEvent_Result{Result: &progressv1.OperationResult{Success: true}},
	})
}

func (s *deployFakeProviderServer) PlanRemoveProject(ctx context.Context, req *contractv1.ProjectRequest) (*planv1.ChangePlan, error) {
	journalEdge(req.GetEdge().GetKind(), nil, nil)
	slug := req.GetSlug()
	if os.Getenv(FakeEmptyRemovalPlanEnvVar) != "" {
		return &planv1.ChangePlan{
			EdgeKind: resolvedEdgeKind(req.GetEdge().GetKind()),
			Subject:  slug,
		}, nil
	}
	if req.GetEnvironment().GetTier() == environmentv1.Tier_TIER_PREVIEW {
		return &planv1.ChangePlan{
			EdgeKind: resolvedEdgeKind(req.GetEdge().GetKind()),
			Subject:  slug,
			Groups: []*planv1.ChangeGroup{
				fakeInfraStackGroup(slug + "--pr-1--infra"),
				fakeInfraStackGroup(slug + "--pr-2--infra"),
				fakeAppStackGroup(slug+"--pr-1--web--b1", "web"),
				{
					Kind:   "edge",
					Name:   resolvedEdgeKind(req.GetEdge().GetKind()) + "/edge",
					Action: planv1.Change_ACTION_DELETE,
					Changes: []*planv1.Change{
						{Kind: edgeRowKind(req.GetEdge().GetKind()), Name: slug, Action: planv1.Change_ACTION_DELETE},
					},
				},
				{
					Kind:   "edge",
					Name:   resolvedEdgeKind(req.GetEdge().GetKind()) + "/edge",
					Action: planv1.Change_ACTION_KEEP,
					Reason: "bootstrap-scoped: every project's previews are served on *.preview.acme.com",
				},
			},
		}, nil
	}
	return &planv1.ChangePlan{
		EdgeKind: resolvedEdgeKind(req.GetEdge().GetKind()),
		Subject:  slug,
		Groups: []*planv1.ChangeGroup{
			fakeInfraStackGroup(slug + "--infra"),
			fakeAppStackGroup(slug+"--web--b1", "web"),
			{
				Kind:   "edge",
				Name:   resolvedEdgeKind(req.GetEdge().GetKind()) + "/edge",
				Action: planv1.Change_ACTION_DELETE,
				Changes: []*planv1.Change{
					{
						Kind:   "Fake::Front",
						Name:   "E1" + slug,
						Action: planv1.Change_ACTION_DISABLE_THEN_DELETE,
						Slow:   true,
					},
					{Kind: "Fake::KeyValueStore", Name: slug + ".example.com", Action: planv1.Change_ACTION_DELETE},
				},
			},
			{
				Kind:   "certificate",
				Name:   slug + ".example.com",
				Action: planv1.Change_ACTION_KEEP,
				Reason: "you pinned this certificate; Ocel never deletes one it did not request",
			},
		},
	}, nil
}

func fakeInfraStackGroup(name string) *planv1.ChangeGroup {
	return &planv1.ChangeGroup{
		Kind:    "stack",
		Name:    "fake/" + name,
		Feature: "infra",
		Action:  planv1.Change_ACTION_DELETE,
		Reason:  "databases and buckets, INCLUDING ALL DATA",
	}
}

func fakeAppStackGroup(name, app string) *planv1.ChangeGroup {
	return &planv1.ChangeGroup{
		Kind:    "stack",
		Name:    "fake/" + name,
		Feature: app,
		Action:  planv1.Change_ACTION_DELETE,
	}
}

func (s *deployFakeProviderServer) RemoveProject(ctx context.Context, req *contractv1.ProjectRequest, stream *connect.ServerStream[progressv1.OperationEvent]) error {
	if err := declareFakeStages(stream); err != nil {
		return err
	}
	journalEdge(req.GetEdge().GetKind(), nil, nil)
	if err := stream.Send(fakeProgress("DESTROY PROJECT project=" + req.GetSlug() + " dns=" + req.GetEdge().GetDns().GetKind() + " " + describeEnv(req.GetEnvironment()) + describeConsent(req.GetConsented()))); err != nil {
		return err
	}
	return stream.Send(&progressv1.OperationEvent{
		Body: &progressv1.OperationEvent_Result{Result: &progressv1.OperationResult{Success: true}},
	})
}

const FakeEnvironmentsEnvVar = "OCEL_TEST_FAKE_ENVIRONMENTS"

func (s *deployFakeProviderServer) ListEnvironments(ctx context.Context, req *contractv1.ListEnvironmentsRequest) (*contractv1.ListEnvironmentsResponse, error) {
	if scripted := os.Getenv(FakeEnvironmentsEnvVar); scripted != "" {
		resp := &contractv1.ListEnvironmentsResponse{}
		if scripted == "none" {
			return resp, nil
		}
		for _, identity := range strings.Split(scripted, ",") {
			resp.Environments = append(resp.Environments, &contractv1.PreviewEnvironment{
				Identity:  identity,
				Lifecycle: environmentv1.Lifecycle_LIFECYCLE_PERSISTENT,
			})
		}
		return resp, nil
	}
	return &contractv1.ListEnvironmentsResponse{
		Environments: []*contractv1.PreviewEnvironment{
			{
				Identity:  "project:" + req.GetSlug(),
				Lifecycle: environmentv1.Lifecycle_LIFECYCLE_EPHEMERAL,
			},
			{
				Identity:  "feature_login_ab12cd34",
				Lifecycle: environmentv1.Lifecycle_LIFECYCLE_EPHEMERAL,
				Label:     "pr-7",
				CreatedAt: 1700000000,
			},
			{
				Identity:  "staging",
				Lifecycle: environmentv1.Lifecycle_LIFECYCLE_PERSISTENT,
			},
		},
	}, nil
}

func describeConsent(consented *planv1.ChangePlan) string {
	var names []string
	for _, group := range consented.GetGroups() {
		names = append(names, group.GetName())
	}
	if len(names) == 0 {
		return " consented=none"
	}
	return " consented=" + strings.Join(names, ",")
}

func describeEnv(env *environmentv1.Environment) string {
	return fmt.Sprintf("tier=%s lifecycle=%s identity=%s",
		env.GetTier(), env.GetLifecycle(), env.GetIdentity())
}

func describeFramework(f *contractv1.Framework) string {
	if f.GetArch() == "" {
		return f.GetName()
	}
	return f.GetName() + "/" + f.GetArch()
}

func describeFunction(app string, f *contractv1.ManifestFunction) string {
	return fmt.Sprintf("logical_name=%s framework=%s entry_file=%s artifact_path=%s app=%s",
		f.GetLogicalName(), describeFramework(f.GetFramework()), f.GetEntryFile(), f.GetArtifactPath(), app)
}

func describeUsage(u *contractv1.ManifestUsage) string {
	return fmt.Sprintf("app=%s resource=%s files=%s", u.GetApp(), u.GetResource(), strings.Join(u.GetFiles(), ","))
}

func describeDelivery(m *contractv1.Manifest, app string) string {
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

func productionHostnames(domains []*contractv1.TierDomains) []string {
	for _, d := range domains {
		if d.GetTier() == environmentv1.Tier_TIER_PRODUCTION {
			return d.GetHostnames()
		}
	}
	return nil
}

func fakeComputes() []string {
	computes := namedComputes(os.Getenv(FakeComputesEnvVar))
	if len(computes) == 0 {
		return []string{"serverless"}
	}
	return computes
}

func namedComputes(named string) []string {
	var computes []string
	for _, compute := range strings.Split(named, ",") {
		if compute = strings.TrimSpace(compute); compute != "" {
			computes = append(computes, compute)
		}
	}
	return computes
}

func describeContainer(app string, c *contractv1.ContainerArtifact) string {
	return fmt.Sprintf("app=%s image=%s health=%s", app, c.GetImage(), c.GetHealthCheckPath())
}

func describeRegistry(r *contractv1.ImageRegistry) string {
	return fmt.Sprintf("server=%s namespace=%s username=%s secret=%v", r.GetServer(), r.GetNamespace(), r.GetUsername(), r.GetPassword() != "")
}

func describeApp(a *contractv1.ManifestApp) string {
	keys := make([]string, 0, len(a.GetVariables()))
	for _, v := range a.GetVariables() {
		keys = append(keys, v.GetKey())
	}
	return fmt.Sprintf("name=%s framework=%s production_domain=%s vars=%s deployment=%s",
		a.GetName(), describeFramework(a.GetFramework()), strings.Join(productionHostnames(a.GetDomains()), ","), strings.Join(keys, ","), a.GetDeploymentId())
}

func parseInfraTier(s string) environmentv1.Tier {
	switch s {
	case "preview":
		return environmentv1.Tier_TIER_PREVIEW
	case "production":
		return environmentv1.Tier_TIER_PRODUCTION
	default:
		return environmentv1.Tier_TIER_UNSPECIFIED
	}
}

var pinnedImage = regexp.MustCompile(`^([^/@:[:space:]]+(:[0-9]+)?/)?[^/@:[:space:]]+(/[^/@:[:space:]]+)*@sha256:[0-9a-f]{64}$`)

func validateFixtureContainer(app string, c *contractv1.ContainerArtifact) error {
	if !pinnedImage.MatchString(c.GetImage()) {
		return fmt.Errorf("container for %s names image %q, and a release pins one repository at one digest", app, c.GetImage())
	}
	if !strings.HasPrefix(c.GetHealthCheckPath(), "/") {
		return fmt.Errorf("container for %s sets health_check_path %q, and a provider is never left to resolve one of its own", app, c.GetHealthCheckPath())
	}
	return nil
}

func validateFixtureManifest(m *contractv1.Manifest) error {
	if m.GetSchemaVersion() == "" {
		return errors.New("manifest missing schema_version")
	}
	for _, a := range m.GetApps() {
		if err := naming.ValidateDeploymentID(a.GetDeploymentId()); err != nil {
			return fmt.Errorf("app %s: %w", a.GetName(), err)
		}
		switch provider.ComputeOf(a) {
		case provider.ComputeContainer:
			if err := validateFixtureContainer(a.GetName(), a.GetContainer()); err != nil {
				return err
			}
		case provider.ComputeServerless:
		default:
			return fmt.Errorf("app %s carries neither functions nor a container image, and a provider only runs %v", a.GetName(), provider.ComputeNames(provider.Computes()))
		}
	}
	declared := map[string]bool{}
	for _, r := range m.GetResources() {
		if r.GetLogicalName() == "" {
			return fmt.Errorf("resource %s has no logical name", r.GetResource().GetType())
		}
		if _, ok := naming.BindableAs(r.GetResource().GetType()); !ok {
			return fmt.Errorf("resource %s has type %v, which names no resource kind", r.GetLogicalName(), r.GetResource().GetType())
		}
		if r.GetResource().GetType() == resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES && r.GetPostgres().GetVersion() != "17" {
			return fmt.Errorf("resource %s postgres version = %q, want %q", r.GetLogicalName(), r.GetPostgres().GetVersion(), "17")
		}
		declared[r.GetLogicalName()] = true
	}
	for _, u := range m.GetUsages() {
		if !declared[u.GetResource()] {
			return fmt.Errorf("usage %s names a resource this manifest never declares", describeUsage(u))
		}
		if len(u.GetFiles()) == 0 {
			return fmt.Errorf("usage %s names no file provenance", describeUsage(u))
		}
	}
	return nil
}
