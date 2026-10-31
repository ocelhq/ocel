package providerhost_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/ocelhq/ocel/pkg/endpoint"
	"github.com/ocelhq/ocel/pkg/environment"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	scopev1 "github.com/ocelhq/ocel/pkg/proto/common/scope/v1"
	endpointv1 "github.com/ocelhq/ocel/pkg/proto/endpoint/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	costv1 "github.com/ocelhq/ocel/pkg/proto/provider/cost/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/provider/providerserver"
	"github.com/ocelhq/ocel/pkg/providerhost"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/scope"
)

const version = "1.2.3"

var everyUserScope = scope.ListUserScopes()

var callers = map[string]endpoint.Caller{
	"ada": mustPerson("org-ada", "user-ada"),
	"bob": mustPerson("org-bob", "user-bob"),
}

func mustPerson(organization, actor string) endpoint.Caller {
	caller, err := endpoint.NewUserCaller("https://console.example.com", organization, actor, scopev1.Role_ROLE_OWNER, everyUserScope)
	if err != nil {
		panic(err)
	}
	return caller
}

type target struct {
	vendor *fake.Provider

	mu        sync.Mutex
	now       time.Time
	opened    []provider.Settings
	confirmed []string
}

func newTarget() *target {
	return &target{vendor: fake.NewProvider(fake.Options{Region: "nowhere"}), now: time.Unix(1_800_000_000, 0)}
}

func (g *target) clock() time.Time {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.now
}

func (g *target) advance(d time.Duration) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.now = g.now.Add(d)
}

func (g *target) listOpened() []provider.Settings {
	g.mu.Lock()
	defer g.mu.Unlock()
	return slices.Clone(g.opened)
}

type options struct {
	named       bool
	asking      bool
	maxSessions int
	refused     []string
}

func everyProcedure() []string {
	var procedures []string
	for _, service := range []protoreflect.ServiceDescriptor{
		contractv1.File_provider_contract_v1_contract_proto.Services().ByName("ProviderService"),
		costv1.File_provider_cost_v1_cost_proto.Services().ByName("CostService"),
	} {
		methods := service.Methods()
		for i := range methods.Len() {
			procedures = append(procedures, "/"+string(service.FullName())+"/"+string(methods.Get(i).Name()))
		}
	}
	return procedures
}

func servedExcept(refused []string) []string {
	return slices.DeleteFunc(everyProcedure(), func(procedure string) bool { return slices.Contains(refused, procedure) })
}

type instance struct {
	host *providerhost.Host
	url  string
}

func startOn(t *testing.T, g *target, name string, o options) *instance {
	t.Helper()
	config := providerhost.Config{
		Version: version,
		Base: func(context.Context, endpoint.Caller) (provider.Settings, error) {
			return provider.Settings{Options: provider.Options{"region": "nowhere"}}, nil
		},
		New: func(_ context.Context, _ endpoint.Caller, _ string, settings provider.Settings) (provider.Provider, error) {
			g.mu.Lock()
			g.opened = append(g.opened, settings)
			g.mu.Unlock()
			if o.asking {
				return askingProvider{Provider: g.vendor, credentials: askingCredentials{Credentials: g.vendor.Credentials(), target: g, host: name}}, nil
			}
			return g.vendor, nil
		},
		Serves:      servedExcept(o.refused),
		Scratch:     t.TempDir(),
		MaxSessions: o.maxSessions,
		IdleAfter:   10 * time.Minute,
		Now:         g.clock,
	}
	if o.named {
		config.Named = &providerhost.NamedSessions{Records: g.vendor.KeyValues(), Cipher: g.vendor.Cipher()}
	}
	host, err := providerhost.New(providerhost.WithQuestionWait(config, time.Second))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(host.Close)
	handler, err := endpoint.New(endpoint.Spec{
		Config:                  endpoint.Config{EndpointID: "endpoint-1", AllowedScopes: scope.ClaimsOf(everyUserScope)},
		ServesManyOrganizations: true,
		Verify: func(_ context.Context, authorization string) (endpoint.Caller, error) {
			caller, ok := callers[strings.TrimPrefix(authorization, "Bearer ")]
			if !ok {
				return endpoint.Caller{}, connect.NewError(connect.CodeUnauthenticated, errors.New("unknown token"))
			}
			return caller, nil
		},
		Mounts: host.Mounts(),
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return &instance{host: host, url: server.URL}
}

type askingProvider struct {
	provider.Provider
	credentials askingCredentials
}

func (p askingProvider) Credentials() provider.Credentials { return p.credentials }

type askingCredentials struct {
	provider.Credentials
	target *target
	host   string
}

func (c askingCredentials) Whoami(ctx context.Context) (provider.Principal, error) {
	c.target.mu.Lock()
	confirmed := len(c.target.confirmed) > 0
	c.target.mu.Unlock()
	if confirmed {
		return c.Credentials.Whoami(ctx)
	}
	return provider.Principal{}, provider.Ask("ada has not said to go ahead", provider.Question{
		Finding: "ada has not said to go ahead",
		Prompt:  "Go ahead?",
		Remedy:  "say to go ahead",
		Confirm: func(context.Context) error {
			c.target.mu.Lock()
			defer c.target.mu.Unlock()
			c.target.confirmed = append(c.target.confirmed, c.host)
			return nil
		},
	})
}

type headers map[string]string

func (i *instance) client(token string, set headers) contractv1connect.ProviderServiceClient {
	return contractv1connect.NewProviderServiceClient(http.DefaultClient, i.url, connect.WithInterceptors(interceptor{func(header http.Header) {
		header.Set("Authorization", "Bearer "+token)
		for name, value := range set {
			header.Set(name, value)
		}
	}}))
}

func sessionless(slug string, tier environment.Tier) headers {
	return headers{providerhost.HeaderSlug: slug, providerhost.HeaderTier: string(tier)}
}

func named(n byte, cliVersion string) headers {
	return headers{providerhost.HeaderSession: strings.Repeat(string("0123456789abcdef"[n%16]), 32), providerhost.HeaderVersion: cliVersion}
}

type interceptor struct{ set func(http.Header) }

func (i interceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, request connect.AnyRequest) (connect.AnyResponse, error) {
		i.set(request.Header())
		return next(ctx, request)
	}
}

func (i interceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return func(ctx context.Context, spec connect.Spec) connect.StreamingClientConn {
		conn := next(ctx, spec)
		i.set(conn.RequestHeader())
		return conn
	}
}

func (i interceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return next
}

func recordDeploy(t *testing.T, g *target, slug string, tier environment.Tier, options map[string]any) {
	t.Helper()
	encoded, err := structpb.NewStruct(options)
	if err != nil {
		t.Fatal(err)
	}
	value, err := protojson.Marshal(&contractv1.DeployedSettings{Options: encoded, Edge: &contractv1.EdgeSelection{Kind: string(fake.KindRelay)}})
	if err != nil {
		t.Fatal(err)
	}
	key := records.Partition{Tier: tier, Root: providerserver.RootDeployedSettings}.Key(slug)
	entry, err := records.ReadOrEmpty(context.Background(), g.vendor.KeyValues(), key)
	if err != nil {
		t.Fatal(err)
	}
	entry.Value = value
	if _, err := g.vendor.KeyValues().Write(context.Background(), entry); err != nil {
		t.Fatal(err)
	}
}

func listPromotions(c contractv1connect.ProviderServiceClient, slug string) error {
	_, err := c.ListPromotions(context.Background(), &contractv1.ListPromotionsRequest{Slug: slug})
	return err
}

func configure(c contractv1connect.ProviderServiceClient) error {
	_, err := c.Configure(context.Background(), &contractv1.ConfigureRequest{Config: &contractv1.ProviderConfig{Slug: "shop", ProjectDir: "/home/ada/shop"}})
	return err
}

func preflight(c contractv1connect.ProviderServiceClient) error {
	stream, err := c.Preflight(context.Background(), &contractv1.PreflightRequest{})
	if err != nil {
		return err
	}
	defer stream.Close()
	for stream.Receive() {
		if stream.Msg().GetResponse() != nil {
			return nil
		}
	}
	return stream.Err()
}

func removeConnector(c contractv1connect.ProviderServiceClient) error {
	stream, err := c.RemoveConnector(context.Background(), &contractv1.RemoveConnectorRequest{})
	if err != nil {
		return err
	}
	defer stream.Close()
	for stream.Receive() {
	}
	return stream.Err()
}

func detail[T proto.Message](err error) (T, bool) {
	var wire *connect.Error
	if errors.As(err, &wire) {
		for _, carried := range wire.Details() {
			if value, err := carried.Value(); err == nil {
				if typed, ok := value.(T); ok {
					return typed, true
				}
			}
		}
	}
	var none T
	return none, false
}

func TestAProcedureTheHostDoesNotServeIsRefusedBeforeAnyProviderOpens(t *testing.T) {
	t.Parallel()
	g := newTarget()
	recordDeploy(t, g, "shop", environment.TierProduction, nil)
	i := startOn(t, g, "a", options{refused: []string{contractv1connect.ProviderServiceRemoveConnectorProcedure}})
	opened := len(g.listOpened())

	err := removeConnector(i.client("ada", sessionless("shop", environment.TierProduction)))
	if code, _ := provider.RefusedCode(err); code != refusal.CodeNeedsRunner {
		t.Errorf("RemoveConnector on a host that does not serve it = %v, want it refused as needing a runner", err)
	}
	if now := len(g.listOpened()); now != opened {
		t.Errorf("the host opened %d providers for a procedure it does not serve, want none", now-opened)
	}
	if err := listPromotions(i.client("ada", sessionless("shop", environment.TierProduction)), "shop"); err != nil {
		t.Errorf("ListPromotions, which the host serves = %v, want it served", err)
	}
}

func TestAHostThatNamesNoProcedureItServesIsRefused(t *testing.T) {
	t.Parallel()
	_, err := providerhost.New(providerhost.Config{
		Base: func(context.Context, endpoint.Caller) (provider.Settings, error) { return provider.Settings{}, nil },
		New: func(context.Context, endpoint.Caller, string, provider.Settings) (provider.Provider, error) {
			return nil, errors.New("never opened")
		},
		Scratch: t.TempDir(),
	})
	if err == nil {
		t.Error("New() with no procedures served = nil, want it refused: a host that names none would serve every procedure a provider gains")
	}
}

func TestACallForAProjectWithNoRecordedDeployNeedsARunner(t *testing.T) {
	t.Parallel()
	g := newTarget()
	i := startOn(t, g, "a", options{})

	err := listPromotions(i.client("ada", sessionless("shop", environment.TierProduction)), "shop")
	if code, _ := provider.RefusedCode(err); code != refusal.CodeNeedsRunner {
		t.Fatalf("ListPromotions for an undeployed project = %v, want it refused as needing a runner", err)
	}
}

func TestACallForADeployedProjectOpensASessionFromTheSettingsItDeployedWith(t *testing.T) {
	t.Parallel()
	g := newTarget()
	recordDeploy(t, g, "shop", environment.TierProduction, map[string]any{"zone": "z1"})
	i := startOn(t, g, "a", options{})
	ada := i.client("ada", sessionless("shop", environment.TierProduction))

	if err := listPromotions(ada, "shop"); err != nil {
		t.Fatalf("ListPromotions for a deployed project = %v, want it served", err)
	}
	if err := listPromotions(ada, "shop"); err != nil {
		t.Fatal(err)
	}
	opened := g.listOpened()
	last := opened[len(opened)-1]
	if last.Slug != "shop" || last.Options["zone"] != "z1" || last.Options["region"] != "nowhere" {
		t.Errorf("the session opened with %+v, want the endpoint's own options overlaid with the ones shop deployed with", last)
	}
	if len(opened) != 2 {
		t.Errorf("the provider was opened %d times for two calls, want once to read the record and once for the session it keeps", len(opened))
	}
}

func TestASessionForOneProjectRefusesARequestNamingAnother(t *testing.T) {
	t.Parallel()
	g := newTarget()
	recordDeploy(t, g, "shop", environment.TierProduction, nil)
	i := startOn(t, g, "a", options{})

	err := listPromotions(i.client("ada", sessionless("shop", environment.TierProduction)), "other")
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("ListPromotions for other through shop's session = %v, want it refused", err)
	}
}

func TestASessionForOneTierRefusesARequestThatActsOnTheOther(t *testing.T) {
	t.Parallel()
	g := newTarget()
	recordDeploy(t, g, "shop", environment.TierProduction, map[string]any{"zone": "z1"})
	recordDeploy(t, g, "shop", environment.TierPreview, map[string]any{"zone": "z2"})
	i := startOn(t, g, "a", options{})
	preview := i.client("ada", sessionless("shop", environment.TierPreview))
	production := i.client("ada", sessionless("shop", environment.TierProduction))
	protection := &contractv1.GetProtectionRequest{Tier: environmentv1.Tier_TIER_PRODUCTION, Slug: "shop"}

	if _, err := preview.GetProtection(context.Background(), protection); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Errorf("GetProtection for production through shop's preview session = %v, want it refused", err)
	}
	if err := listPromotions(preview, "shop"); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Errorf("ListPromotions, which reads production, through shop's preview session = %v, want it refused", err)
	}
	if _, err := production.GetProtection(context.Background(), protection); err != nil {
		t.Errorf("GetProtection for production through shop's production session = %v, want it served", err)
	}
	if err := listPromotions(production, "shop"); err != nil {
		t.Errorf("ListPromotions through shop's production session = %v, want it served", err)
	}
}

func TestACallNamingNoProjectIsServedFromTheEndpointsOwnSettings(t *testing.T) {
	t.Parallel()
	g := newTarget()
	i := startOn(t, g, "a", options{})

	if _, err := i.client("ada", nil).ListProjects(context.Background(), &contractv1.ListProjectsRequest{}); err != nil {
		t.Fatalf("ListProjects with no session = %v, want it served", err)
	}
	if opened := g.listOpened(); opened[len(opened)-1].Slug != "" || opened[len(opened)-1].Options["region"] != "nowhere" {
		t.Errorf("the session opened with %+v, want the endpoint's own settings", opened[len(opened)-1])
	}
}

func TestACallWithNoSessionConfiguresNothing(t *testing.T) {
	t.Parallel()
	i := startOn(t, newTarget(), "a", options{})

	if err := configure(i.client("ada", sessionless("shop", environment.TierProduction))); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("Configure with no session = %v, want invalid argument", err)
	}
}

func TestAProjectHeaderNamingNoTierIsRefused(t *testing.T) {
	t.Parallel()
	i := startOn(t, newTarget(), "a", options{})

	for _, set := range []headers{
		{providerhost.HeaderSlug: "shop"},
		{providerhost.HeaderTier: "production"},
		{providerhost.HeaderSlug: "Shop!", providerhost.HeaderTier: "production"},
		{providerhost.HeaderSlug: "shop", providerhost.HeaderTier: "staging"},
	} {
		if err := listPromotions(i.client("ada", set), "shop"); connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Errorf("ListPromotions with %v = %v, want invalid argument", set, err)
		}
	}
}

func TestACallWithNoSessionIsNotHeldToTheVersionServed(t *testing.T) {
	t.Parallel()
	g := newTarget()
	recordDeploy(t, g, "shop", environment.TierProduction, nil)
	i := startOn(t, g, "a", options{named: true})

	set := sessionless("shop", environment.TierProduction)
	set[providerhost.HeaderVersion] = "0.0.1"
	if err := listPromotions(i.client("ada", set), "shop"); err != nil {
		t.Fatalf("a sessionless call naming another version = %v, want it served", err)
	}
}

func TestEachOrganizationHasItsOwnSessionForAProject(t *testing.T) {
	t.Parallel()
	g := newTarget()
	recordDeploy(t, g, "shop", environment.TierProduction, nil)
	i := startOn(t, g, "a", options{})

	for _, token := range []string{"ada", "bob", "ada"} {
		if err := listPromotions(i.client(token, sessionless("shop", environment.TierProduction)), "shop"); err != nil {
			t.Fatal(err)
		}
	}
	if opened := len(g.listOpened()); opened != 4 {
		t.Errorf("the provider was opened %d times, want twice for each organization's session", opened)
	}
}

func TestAtItsBoundTheLeastRecentlyUsedIdleSessionEnds(t *testing.T) {
	t.Parallel()
	g := newTarget()
	recordDeploy(t, g, "shop", environment.TierProduction, nil)
	recordDeploy(t, g, "blog", environment.TierProduction, nil)
	i := startOn(t, g, "a", options{maxSessions: 1})

	for _, slug := range []string{"shop", "blog", "shop"} {
		if err := listPromotions(i.client("ada", sessionless(slug, environment.TierProduction)), slug); err != nil {
			t.Fatalf("ListPromotions for %s at the bound = %v, want the idle session to make room", slug, err)
		}
	}
	if opened := len(g.listOpened()); opened != 6 {
		t.Errorf("the provider was opened %d times, want a session opened afresh for each call that found its own let go", opened)
	}
}

func TestAnEndpointWithNoNamedSessionsRefusesOne(t *testing.T) {
	t.Parallel()
	i := startOn(t, newTarget(), "a", options{})

	if err := configure(i.client("ada", named(1, version))); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("Configure on a named session at an endpoint with none = %v, want invalid argument", err)
	}
}

func TestANamedSessionIsHeldToTheVersionServed(t *testing.T) {
	t.Parallel()
	i := startOn(t, newTarget(), "a", options{named: true})

	for _, cliVersion := range []string{"", "1.2.2"} {
		err := configure(i.client("ada", named(1, cliVersion)))
		refused, ok := detail[*endpointv1.VersionUnserved](err)
		if connect.CodeOf(err) != connect.CodeFailedPrecondition || !ok || !slices.Equal(refused.GetServed(), []string{version}) {
			t.Errorf("Configure from ocel %q = %v, want it refused naming %s", cliVersion, err, version)
		}
	}
}

func TestANamedSessionIsConfiguredOnceAndBelongsToItsOrganization(t *testing.T) {
	t.Parallel()
	g := newTarget()
	i := startOn(t, g, "a", options{named: true})
	ada := i.client("ada", named(1, version))

	if err := configure(ada); err != nil {
		t.Fatalf("Configure = %v", err)
	}
	if err := listPromotions(ada, "shop"); err != nil {
		t.Fatalf("ListPromotions on the configured session = %v", err)
	}
	if err := configure(ada); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Errorf("a second Configure = %v, want failed precondition", err)
	}
	if !i.host.HoldsSession(callers["ada"], strings.Repeat("1", 32)) {
		t.Error("HoldsSession = false for the session ada configured")
	}
	if i.host.HoldsSession(callers["bob"], strings.Repeat("1", 32)) {
		t.Error("HoldsSession = true for bob on ada's session")
	}
	if err := listPromotions(i.client("bob", named(1, version)), "shop"); err == nil {
		t.Error("bob's call on ada's session id was served from ada's session")
	}
}

func TestANamedSessionConfiguredOnOneInstanceServesItsCallsOnAnother(t *testing.T) {
	t.Parallel()
	g := newTarget()
	a, b := startOn(t, g, "a", options{named: true}), startOn(t, g, "b", options{named: true})

	if err := configure(a.client("ada", named(1, version))); err != nil {
		t.Fatal(err)
	}
	if err := listPromotions(b.client("ada", named(1, version)), "shop"); err != nil {
		t.Fatalf("a call on the instance that never saw the Configure = %v", err)
	}
	if err := listPromotions(b.client("ada", named(2, version)), "shop"); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("a call on a session nobody configured = %v, want it refused as ended", err)
	} else if _, ended := detail[*endpointv1.SessionEnded](err); !ended {
		t.Fatalf("a call on a session nobody configured = %v, want a SessionEnded detail", err)
	}
}

func TestANamedSessionsRecordEndsAfterItsIdleTime(t *testing.T) {
	t.Parallel()
	g := newTarget()
	a, b := startOn(t, g, "a", options{named: true}), startOn(t, g, "b", options{named: true})
	if err := configure(a.client("ada", named(1, version))); err != nil {
		t.Fatal(err)
	}
	g.advance(11 * time.Minute)
	err := listPromotions(b.client("ada", named(1, version)), "shop")
	if _, ended := detail[*endpointv1.SessionEnded](err); !ended {
		t.Fatalf("a call on a session idle for 11 minutes = %v, want it ended", err)
	}
}

func TestAQuestionAskedOnOneInstanceIsAnsweredThroughAnother(t *testing.T) {
	t.Parallel()
	g := newTarget()
	recordDeploy(t, g, "shop", environment.TierProduction, nil)
	a, b := startOn(t, g, "a", options{asking: true}), startOn(t, g, "b", options{asking: true})

	question, ok := detail[*contractv1.Question](preflight(a.client("ada", sessionless("shop", environment.TierProduction))))
	if !ok {
		t.Fatal("Preflight asked no question")
	}
	if _, err := b.client("ada", sessionless("shop", environment.TierProduction)).Confirm(context.Background(), &contractv1.ConfirmRequest{QuestionId: question.GetId()}); err != nil {
		t.Fatalf("confirming on another instance = %v", err)
	}
	g.mu.Lock()
	confirmed := slices.Clone(g.confirmed)
	g.mu.Unlock()
	if !slices.Equal(confirmed, []string{"a"}) {
		t.Fatalf("the question was confirmed on %v, want once, on the instance that asked it", confirmed)
	}
	if left, err := g.vendor.KeyValues().List(context.Background(), providerserver.QuestionsPartition(environment.TierProduction)); err != nil || len(left) > 0 {
		t.Errorf("a relayed question left %d records (%v)", len(left), err)
	}
}

func TestAQuestionNoInstanceHoldsIsRefusedAsNeverAsked(t *testing.T) {
	t.Parallel()
	g := newTarget()
	recordDeploy(t, g, "shop", environment.TierProduction, nil)
	a, b := startOn(t, g, "a", options{asking: true}), startOn(t, g, "b", options{asking: true})

	question, ok := detail[*contractv1.Question](preflight(a.client("ada", sessionless("shop", environment.TierProduction))))
	if !ok {
		t.Fatal("Preflight asked no question")
	}
	a.host.Close()
	_, err := b.client("ada", sessionless("shop", environment.TierProduction)).Confirm(context.Background(), &contractv1.ConfirmRequest{QuestionId: question.GetId()})
	if code, _ := provider.RefusedCode(err); code != refusal.CodeInvalid {
		t.Fatalf("confirming a question its instance took with it = %v, want it refused as invalid", err)
	}
}

func TestACallNamingNoProjectServesNoRequestThatNamesOne(t *testing.T) {
	t.Parallel()
	g := newTarget()
	recordDeploy(t, g, "shop", environment.TierProduction, nil)
	i := startOn(t, g, "a", options{})

	if err := listPromotions(i.client("ada", nil), "shop"); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("ListPromotions for shop with no %s = %v, want it refused: the session read no project's deploy", providerhost.HeaderSlug, err)
	}
}

func TestADeployRecordingNewSettingsReopensTheSessionTheOldOnesOpened(t *testing.T) {
	t.Parallel()
	g := newTarget()
	recordDeploy(t, g, "shop", environment.TierProduction, map[string]any{"zone": "z1"})
	i := startOn(t, g, "a", options{})
	ada := i.client("ada", sessionless("shop", environment.TierProduction))
	if err := listPromotions(ada, "shop"); err != nil {
		t.Fatal(err)
	}

	recordDeploy(t, g, "shop", environment.TierProduction, map[string]any{"zone": "z2"})
	if err := listPromotions(ada, "shop"); err != nil {
		t.Fatal(err)
	}
	opened := g.listOpened()
	if zone := opened[len(opened)-1].Options["zone"]; zone != "z2" {
		t.Errorf("the session serving after the second deploy opened with zone %v, want z2, what that deploy recorded", zone)
	}
}
