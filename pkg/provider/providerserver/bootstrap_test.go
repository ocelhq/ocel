package providerserver_test

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	connect "connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	planv1 "github.com/ocelhq/ocel/pkg/proto/common/plan/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/provider/providerserver"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

func contractServed(t *testing.T, version string) (contractv1connect.ProviderServiceClient, *fake.Provider) {
	t.Helper()

	vendor := fake.NewProvider(fake.Options{Region: "nowhere"}).WithProjectDir(workingDir(t))
	return servedProvider(t, version, vendor), vendor
}

func servedProvider(t *testing.T, version string, p provider.Provider) contractv1connect.ProviderServiceClient {
	t.Helper()

	client, _ := configuredProvider(t, version, p)
	return client
}

func configuredProvider(t *testing.T, version string, p provider.Provider) (contractv1connect.ProviderServiceClient, *contractv1.ProviderFacts) {
	t.Helper()

	config := providerserver.Config{
		Version: version,
		New: func(context.Context, provider.Settings) (provider.Provider, error) {
			return p, nil
		},
	}
	server := httptest.NewServer(providerserver.ConformanceMux(config))
	t.Cleanup(server.Close)

	client := contractv1connect.NewProviderServiceClient(server.Client(), server.URL)
	configured, err := client.Configure(context.Background(), configureInWorkingDir(t))
	if err != nil {
		t.Fatalf("Configure() error = %v", err)
	}
	return client, configured.GetFacts()
}

func drain(stream *connect.ServerStreamForClient[progressv1.OperationEvent]) (*progressv1.OperationResult, error) {
	defer stream.Close()
	var result *progressv1.OperationResult
	for stream.Receive() {
		if event := stream.Msg().GetResult(); event != nil {
			result = event
		}
	}
	if err := stream.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

func bootstrapOK(t *testing.T, client contractv1connect.ProviderServiceClient, req *contractv1.BootstrapRequest) {
	t.Helper()

	stream, err := client.Bootstrap(context.Background(), req)
	if err != nil {
		t.Fatalf("Bootstrap() error = %v", err)
	}
	result, err := drain(stream)
	if err != nil {
		t.Fatalf("Bootstrap() stream = %v", err)
	}
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Bootstrap() result error = %q, want it to succeed", result.GetError())
	}
}

func TestBootstrapAddsThePartItsEdgeRequiresAndRefusesReplacementsUnlessAsked(t *testing.T) {
	t.Parallel()

	client, vendor := contractServed(t, "1.2.3")
	bootstrapOK(t, client, &contractv1.BootstrapRequest{
		Tier: environmentv1.Tier_TIER_PRODUCTION,
	})

	applied := vendor.FakeBootstrap().Applied()
	if len(applied) != 1 {
		t.Fatalf("Apply() ran %d times, want once", len(applied))
	}
	if want := []string{fake.PartImages}; !slices.Equal(applied[0].Parts, want) {
		t.Errorf("Apply() was asked for %v, want %v", applied[0].Parts, want)
	}
	if applied[0].Tier != environment.TierProduction {
		t.Errorf("Apply() ran against %s, want %s", applied[0].Tier, environment.TierProduction)
	}
	if !applied[0].RefuseReplacements {
		t.Error("Apply() was allowed to replace resources where nothing accepted replacements")
	}
}

func TestBootstrapAcceptsReplacementsWhenTheRequestDoes(t *testing.T) {
	t.Parallel()

	client, vendor := contractServed(t, "1.2.3")
	bootstrapOK(t, client, &contractv1.BootstrapRequest{
		Tier:               environmentv1.Tier_TIER_PRODUCTION,
		AcceptReplacements: true,
	})

	applied := vendor.FakeBootstrap().Applied()
	if applied[0].RefuseReplacements {
		t.Error("Apply() refused replacements where the request accepted them")
	}
}

func TestBootstrapRefusesToRemoveAPartThisProviderDoesNotOffer(t *testing.T) {
	t.Parallel()

	client, _ := contractServed(t, "1.2.3")
	stream, err := client.Bootstrap(context.Background(), &contractv1.BootstrapRequest{
		Tier:   environmentv1.Tier_TIER_PRODUCTION,
		Remove: []string{"quantum-edge"},
	})
	if err == nil {
		_, err = drain(stream)
	}
	if got := connect.CodeOf(err); got != connect.CodeInvalidArgument {
		t.Fatalf("Bootstrap() removing an unknown part: code = %v, want %v (%v)", got, connect.CodeInvalidArgument, err)
	}
	if !strings.Contains(err.Error(), fake.PartCache) {
		t.Errorf("Bootstrap() = %v, want it to name what this provider does offer", err)
	}
}

func TestBootstrapRefusesToRemoveAPartAProjectRecorded(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client, vendor := contractServed(t, "1.2.3")
	bootstrapOK(t, client, &contractv1.BootstrapRequest{
		Tier: environmentv1.Tier_TIER_PRODUCTION,
	})
	recordProject(t, vendor, "shop", fake.PartImages)

	stream, err := client.Bootstrap(ctx, &contractv1.BootstrapRequest{
		Tier:   environmentv1.Tier_TIER_PRODUCTION,
		Edge:   &contractv1.EdgeSelection{Kind: string(fake.KindDirect)},
		Remove: []string{fake.PartImages},
	})
	if err != nil {
		t.Fatalf("Bootstrap() error = %v", err)
	}
	result, err := drain(stream)
	if err != nil {
		t.Fatalf("Bootstrap() stream = %v", err)
	}
	if result.GetSuccess() {
		t.Fatal("Bootstrap() removed a part a deployed project recorded")
	}
	for _, want := range []string{fake.PartImages, "shop", "--force"} {
		if !strings.Contains(result.GetError(), want) {
			t.Errorf("Bootstrap() refused with %q, want it to contain %q", result.GetError(), want)
		}
	}
}

func TestBootstrapRemovesWhatItIsToldWhenForced(t *testing.T) {
	t.Parallel()

	client, vendor := contractServed(t, "1.2.3")
	bootstrapOK(t, client, &contractv1.BootstrapRequest{
		Tier: environmentv1.Tier_TIER_PRODUCTION,
	})
	bootstrapped(t, vendor, environment.TierProduction, fake.PartCache)
	recordProject(t, vendor, "shop", fake.PartImages)

	bootstrapOK(t, client, &contractv1.BootstrapRequest{
		Tier:   environmentv1.Tier_TIER_PRODUCTION,
		Edge:   &contractv1.EdgeSelection{Kind: string(fake.KindDirect)},
		Remove: []string{fake.PartImages, fake.PartCache},
		Force:  true,
	})

	applied := vendor.FakeBootstrap().Applied()
	removing := applied[len(applied)-1]
	if want := []string{fake.PartCache, fake.PartImages}; !slices.Equal(removing.Remove, want) {
		t.Errorf("Apply() removed %v, want %v, the parts named, in the order the provider offers them", removing.Remove, want)
	}
	if len(removing.Parts) != 0 {
		t.Errorf("Apply() was asked for %v, want a run that named only removals to ensure nothing", removing.Parts)
	}
}

func TestBootstrapLeavesAPartNoRunNamed(t *testing.T) {
	t.Parallel()

	client, vendor := contractServed(t, "1.2.3")
	bootstrapOK(t, client, &contractv1.BootstrapRequest{
		Tier: environmentv1.Tier_TIER_PRODUCTION,
	})
	bootstrapOK(t, client, &contractv1.BootstrapRequest{
		Tier: environmentv1.Tier_TIER_PRODUCTION,
		Edge: &contractv1.EdgeSelection{Kind: string(fake.KindDirect)},
	})

	applied := vendor.FakeBootstrap().Applied()
	last := applied[len(applied)-1]
	if len(last.Remove) != 0 {
		t.Errorf("Apply() removed %v, want a run naming only %s to leave the rest of the account alone",
			last.Remove, fake.PartCache)
	}
}

func recordProject(t *testing.T, vendor *fake.Provider, slug string, parts ...string) {
	t.Helper()

	body, err := json.Marshal(stackrecords.Project{Parts: parts})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := vendor.KeyValues().Write(context.Background(), keyvalue.Entry{
		Key:   stackrecords.ProjectKey(environment.TierProduction, slug),
		Value: body,
	}); err != nil {
		t.Fatal(err)
	}
}

func TestDescribeBootstrapAnswersTheCurrentStatus(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client, p := contractServed(t, "1.2.3")
	bootstrapped(t, p, environment.TierProduction, fake.PartCache)
	bootstrapOK(t, client, &contractv1.BootstrapRequest{
		Tier: environmentv1.Tier_TIER_PRODUCTION,
		Edge: &contractv1.EdgeSelection{Kind: string(fake.KindDirect)},
	})

	planned, err := client.DescribeBootstrap(ctx, &contractv1.DescribeBootstrapRequest{
		Tier: environmentv1.Tier_TIER_PRODUCTION,
		Edge: &contractv1.EdgeSelection{Kind: string(fake.KindDirect)},
	})
	if err != nil {
		t.Fatalf("DescribeBootstrap() error = %v", err)
	}

	status := planned.GetBootstrap()
	if !status.GetPresent() {
		t.Errorf("DescribeBootstrap() status = %+v, want it present", status)
	}
	if status.GetWriter() != "1.2.3" {
		t.Errorf("writer = %q, want the version this provider was built as", status.GetWriter())
	}
	for _, stack := range status.GetStacks() {
		if !stack.GetRequired() {
			t.Errorf("stack %s is not required, and everything installed was asked for", stack.GetName())
		}
		if stack.GetWrittenBy() == "" {
			t.Errorf("stack %s says nobody wrote it", stack.GetName())
		}
	}
}

func streamedPlan(t *testing.T, client contractv1connect.ProviderServiceClient, req *contractv1.BootstrapRequest) *planv1.ChangePlan {
	t.Helper()

	stream, err := client.Bootstrap(context.Background(), req)
	if err != nil {
		t.Fatalf("Bootstrap() error = %v", err)
	}
	defer stream.Close()
	var plan *planv1.ChangePlan
	for stream.Receive() {
		if shown := stream.Msg().GetPlan(); shown != nil {
			plan = shown
		}
	}
	if err := stream.Err(); err != nil {
		t.Fatalf("Bootstrap() stream = %v", err)
	}
	return plan
}

func streamedEvents(t *testing.T, client contractv1connect.ProviderServiceClient, req *contractv1.BootstrapRequest) ([]*progressv1.OperationEvent, error) {
	t.Helper()

	stream, err := client.Bootstrap(context.Background(), req)
	if err != nil {
		t.Fatalf("Bootstrap() error = %v", err)
	}
	defer stream.Close()
	var events []*progressv1.OperationEvent
	for stream.Receive() {
		events = append(events, stream.Msg())
	}
	return events, stream.Err()
}

func TestAnApplyWithAConsentedPlanDrawsNoPlanOfItsOwn(t *testing.T) {
	t.Parallel()

	client, _ := contractServed(t, "1.2.3")
	req := &contractv1.BootstrapRequest{
		Tier: environmentv1.Tier_TIER_PRODUCTION,
		Dry:  true,
	}
	consented := streamedPlan(t, client, req)
	if consented == nil {
		t.Fatal("the dry run streamed no plan, and the only thing consented to is the plan")
	}

	req.Dry, req.Consented = false, consented
	events, err := streamedEvents(t, client, req)
	if err != nil {
		t.Fatalf("Bootstrap() applying the consented plan = %v", err)
	}
	for _, ev := range events {
		if ev.GetPlan() != nil {
			t.Errorf("the apply drew a plan of its own, want the run to show one plan — the one it was handed")
		}
	}
}

func TestAnApplyRefusesWorkTheConsentedPlanNeverShowed(t *testing.T) {
	t.Parallel()

	client, vendor := contractServed(t, "1.2.3")
	bootstrapped(t, vendor, environment.TierProduction, fake.PartImages)

	req := &contractv1.BootstrapRequest{
		Tier: environmentv1.Tier_TIER_PRODUCTION,
		Dry:  true,
	}
	consented := streamedPlan(t, client, req)
	if consented == nil {
		t.Fatal("the dry run streamed no plan, and the only thing consented to is the plan")
	}
	vendor.FakeBootstrap().MarkStale(fake.PartImages)

	req.Dry, req.Consented = false, consented
	_, err := streamedEvents(t, client, req)
	if err == nil {
		t.Fatal("Bootstrap() = nil, want an apply that outgrew its consented plan refused")
	}
	if !strings.Contains(err.Error(), fake.PartImages) {
		t.Errorf("the refusal reads %q, want it to name what moved under the plan", err)
	}
}

func TestAnApplyRefusesAConsentedPlanItCannotRead(t *testing.T) {
	t.Parallel()

	client, _ := contractServed(t, "1.2.3")
	unreadable := planv1.Change_Action(len(planv1.Change_Action_name) + 40)
	_, err := streamedEvents(t, client, &contractv1.BootstrapRequest{
		Tier: environmentv1.Tier_TIER_PRODUCTION,
		Consented: &planv1.ChangePlan{Groups: []*planv1.ChangeGroup{
			{Kind: provider.StackGroupKind, Name: "core", Action: unreadable},
		}},
	})
	if err == nil {
		t.Fatal("Bootstrap() = nil, want an apply naming an action providerserver cannot read refused")
	}
	if !strings.Contains(err.Error(), unreadable.String()) {
		t.Errorf("the refusal reads %q, want it to name the action it could not read", err)
	}
}

func TestARemovalRefusesWorkTheConsentedPlanNeverShowed(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client, vendor := contractServed(t, "1.2.3")
	bootstrapped(t, vendor, environment.TierProduction, fake.PartCache)

	scope := &contractv1.BootstrapScope{Tier: environmentv1.Tier_TIER_PRODUCTION}
	consented, err := client.PlanRemoveBootstrap(ctx, scope)
	if err != nil {
		t.Fatalf("PlanRemoveBootstrap() error = %v", err)
	}

	bootstrapped(t, vendor, environment.TierProduction, fake.PartCache, fake.PartImages)

	scope.Consented = consented
	stream, err := client.RemoveBootstrap(ctx, scope)
	if err != nil {
		t.Fatalf("RemoveBootstrap() error = %v", err)
	}
	defer stream.Close()
	for stream.Receive() {
	}
	if stream.Err() == nil {
		t.Fatal("RemoveBootstrap() = nil, want a removal that outgrew its consented plan refused")
	}
	if !strings.Contains(stream.Err().Error(), fake.PartImages) {
		t.Errorf("the refusal reads %q, want it to name what the plan installed", stream.Err())
	}
}

func TestBootstrapShowsThePlanItIsAboutToApply(t *testing.T) {
	t.Parallel()

	client, vendor := contractServed(t, "1.2.3")
	plan := streamedPlan(t, client, &contractv1.BootstrapRequest{
		Tier: environmentv1.Tier_TIER_PRODUCTION,
	})
	if plan == nil {
		t.Fatal("Bootstrap() streamed no plan, and the only thing consented to is the plan")
	}
	if plan.GetSubject() != string(environment.TierProduction) {
		t.Errorf("plan subject = %q, want the tier it applies to", plan.GetSubject())
	}
	if plan.GetEdgeKind() != string(fake.KindRelay) {
		t.Errorf("plan was drawn against the %q edge, want the default the apply bootstraps", plan.GetEdgeKind())
	}
	if len(plan.GetGroups()) != 2 {
		t.Fatalf("plan = %v, want the baseline and images, the part the relay edge requires", plan.GetGroups())
	}
	for _, group := range plan.GetGroups() {
		if group.GetAction() != planv1.Change_ACTION_CREATE {
			t.Errorf("%s is %s, want it created on an empty account", group.GetName(), group.GetAction())
		}
		if len(group.GetChanges()) == 0 {
			t.Errorf("%s has no resource-level detail", group.GetName())
		}
	}
	if len(vendor.FakeBootstrap().Applied()) != 1 {
		t.Errorf("the bootstrap was applied %d times, want the one this stream ran",
			len(vendor.FakeBootstrap().Applied()))
	}
}

func TestADryBootstrapDrawsThePlanAndProvisionsNothing(t *testing.T) {
	t.Parallel()

	client, vendor := contractServed(t, "1.2.3")
	plan := streamedPlan(t, client, &contractv1.BootstrapRequest{
		Tier: environmentv1.Tier_TIER_PRODUCTION,
		Dry:  true,
	})
	if plan == nil || len(plan.GetGroups()) == 0 {
		t.Fatal("a dry bootstrap streamed no plan, and drawing the plan is all it is for")
	}
	if applied := vendor.FakeBootstrap().Applied(); len(applied) != 0 {
		t.Errorf("a dry bootstrap applied %v, want it to change nothing", applied)
	}
}

func TestDescribeBootstrapReportsADowngrade(t *testing.T) {
	t.Parallel()

	client, vendor := contractServed(t, "1.0.0")
	bootstrapOK(t, client, &contractv1.BootstrapRequest{Tier: environmentv1.Tier_TIER_PRODUCTION})
	vendor.FakeBootstrap().SetWriter("2.0.0")

	planned, err := client.DescribeBootstrap(context.Background(), &contractv1.DescribeBootstrapRequest{
		Tier: environmentv1.Tier_TIER_PRODUCTION,
	})
	if err != nil {
		t.Fatalf("DescribeBootstrap() error = %v", err)
	}
	if !planned.GetBootstrap().GetDowngrade() {
		t.Error("DescribeBootstrap() reports no downgrade where a newer build wrote the bootstrap")
	}
}

func TestGetCredentialPermissionsRendersEitherPurpose(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client, _ := contractServed(t, "1.2.3")

	for purpose, want := range map[contractv1.CredentialPurpose]string{
		contractv1.CredentialPurpose_CREDENTIAL_PURPOSE_BOOTSTRAP: string(edge.PurposeBootstrap),
		contractv1.CredentialPurpose_CREDENTIAL_PURPOSE_DEPLOY:    string(edge.PurposeDeploy),
	} {
		permissions, err := client.GetCredentialPermissions(ctx, &contractv1.CredentialPermissionsRequest{Purpose: purpose, Tier: environmentv1.Tier_TIER_PRODUCTION})
		if err != nil {
			t.Fatalf("GetCredentialPermissions(%v) error = %v", purpose, err)
		}
		groups := permissions.GetGroups()
		if len(groups) != 1 {
			t.Fatalf("GetCredentialPermissions(%v) rendered %d groups, want only the vendor's", purpose, len(groups))
		}
		if got := groups[0].GetHeading(); got != "fake credentials" {
			t.Errorf("GetCredentialPermissions(%v) heading = %q, want the vendor's own heading", purpose, got)
		}
		if !strings.Contains(groups[0].GetDocument(), want) {
			t.Errorf("GetCredentialPermissions(%v) = %q, want it to render the %s purpose", purpose, groups[0].GetDocument(), want)
		}
	}

	_, err := client.GetCredentialPermissions(ctx, &contractv1.CredentialPermissionsRequest{Tier: environmentv1.Tier_TIER_PRODUCTION})
	if got := connect.CodeOf(err); got != connect.CodeInvalidArgument {
		t.Fatalf("GetCredentialPermissions() naming no purpose: code = %v, want %v", got, connect.CodeInvalidArgument)
	}
}

func TestGetCredentialPermissionsRendersTheTierItIsAskedFor(t *testing.T) {
	t.Parallel()

	client, _ := contractServed(t, "1.2.3")

	for tier, want := range map[environmentv1.Tier]string{
		environmentv1.Tier_TIER_PRODUCTION: string(environment.TierProduction),
		environmentv1.Tier_TIER_PREVIEW:    string(environment.TierPreview),
	} {
		permissions, err := client.GetCredentialPermissions(context.Background(), &contractv1.CredentialPermissionsRequest{
			Purpose: contractv1.CredentialPurpose_CREDENTIAL_PURPOSE_DEPLOY,
			Tier:    tier,
		})
		if err != nil {
			t.Fatalf("GetCredentialPermissions(%v) error = %v", tier, err)
		}
		if got := permissions.GetGroups()[0].GetDocument(); !strings.HasSuffix(got, "in "+want) {
			t.Errorf("GetCredentialPermissions(%v) = %q, want the vendor asked for the %s tier", tier, got, want)
		}
	}
}

func TestGetCredentialPermissionsRefusesARequestNamingNoTier(t *testing.T) {
	t.Parallel()

	client, _ := contractServed(t, "1.2.3")

	_, err := client.GetCredentialPermissions(context.Background(), &contractv1.CredentialPermissionsRequest{
		Purpose: contractv1.CredentialPurpose_CREDENTIAL_PURPOSE_DEPLOY,
	})
	if got := connect.CodeOf(err); got != connect.CodeInvalidArgument {
		t.Fatalf("GetCredentialPermissions() naming no tier: code = %v (%v), want %v: it would render production's credential for a caller that asked for none", got, err, connect.CodeInvalidArgument)
	}
}

func TestGetCredentialPermissionsAppendsWhatTheEdgeDocuments(t *testing.T) {
	t.Parallel()

	vendor := fake.NewProvider(fake.Options{Region: "nowhere"})
	client := servedProvider(t, "1.2.3", documentingProvider{Provider: vendor})

	permissions, err := client.GetCredentialPermissions(context.Background(), &contractv1.CredentialPermissionsRequest{
		Purpose: contractv1.CredentialPurpose_CREDENTIAL_PURPOSE_DEPLOY,
		Tier:    environmentv1.Tier_TIER_PRODUCTION,
		Edge:    &contractv1.EdgeSelection{Kind: string(fake.KindDirect)},
	})
	if err != nil {
		t.Fatalf("GetCredentialPermissions() error = %v", err)
	}
	groups := permissions.GetGroups()
	if len(groups) != 2 {
		t.Fatalf("GetCredentialPermissions() rendered %d groups, want the vendor's and the edge's", len(groups))
	}
	if got := groups[1].GetHeading(); got != documentedHeading {
		t.Errorf("GetCredentialPermissions() second heading = %q, want %q", got, documentedHeading)
	}
	if got := groups[1].GetDocument(); got != string(edge.PurposeDeploy) {
		t.Errorf("GetCredentialPermissions() second document = %q, want the edge asked for the %s purpose", got, edge.PurposeDeploy)
	}
}

func TestGetCredentialPermissionsRefusesAnOptionTheEdgeDoesNotTake(t *testing.T) {
	t.Parallel()

	vendor := fake.NewProvider(fake.Options{Region: "nowhere"})
	client := servedProvider(t, "1.2.3", documentingProvider{Provider: vendor})
	options, err := structpb.NewStruct(map[string]any{"tunel": true})
	if err != nil {
		t.Fatal(err)
	}

	_, err = client.GetCredentialPermissions(context.Background(), &contractv1.CredentialPermissionsRequest{
		Purpose: contractv1.CredentialPurpose_CREDENTIAL_PURPOSE_DEPLOY,
		Tier:    environmentv1.Tier_TIER_PRODUCTION,
		Edge:    &contractv1.EdgeSelection{Kind: string(fake.KindDirect), Options: options},
	})
	if got := connect.CodeOf(err); got != connect.CodeInvalidArgument {
		t.Fatalf("GetCredentialPermissions() with an option the edge does not take: code = %v (%v), want %v", got, err, connect.CodeInvalidArgument)
	}
	if !strings.Contains(err.Error(), "tunel") {
		t.Errorf("GetCredentialPermissions() error = %v, want it to name the option", err)
	}
}

func TestGetCredentialPermissionsRendersOnlyTheVendorsGroupForAnEdgeTheProviderDoesNotServe(t *testing.T) {
	t.Parallel()

	vendor := fake.NewProvider(fake.Options{Region: "nowhere"})
	client := servedProvider(t, "1.2.3", documentingProvider{Provider: vendor})

	permissions, err := client.GetCredentialPermissions(context.Background(), &contractv1.CredentialPermissionsRequest{
		Purpose: contractv1.CredentialPurpose_CREDENTIAL_PURPOSE_DEPLOY,
		Tier:    environmentv1.Tier_TIER_PRODUCTION,
		Edge:    &contractv1.EdgeSelection{Kind: "elsewhere"},
	})
	if err != nil {
		t.Fatalf("GetCredentialPermissions() error = %v", err)
	}
	if groups := permissions.GetGroups(); len(groups) != 1 {
		t.Fatalf("GetCredentialPermissions() rendered %d groups, want only the vendor's", len(groups))
	}
}

const documentedHeading = "an edge token"

type documentingProvider struct {
	provider.Provider
}

func (p documentingProvider) Edges() provider.Edges {
	return documentingEdges{Edges: p.Provider.Edges()}
}

type documentingEdges struct {
	provider.Edges
}

func (e documentingEdges) Open(kind edge.Kind, options provider.Options) (edge.Edge, error) {
	front, err := e.Edges.Open(kind, options)
	if err != nil {
		return nil, err
	}
	return documentingEdge{Edge: front}, nil
}

type documentingEdge struct {
	edge.Edge
}

func (e documentingEdge) Hooks() edge.Hooks {
	hooks := e.Edge.Hooks()
	hooks.DescribeCredentialPermissions = func(purpose edge.CredentialPurpose) (edge.CredentialDocument, error) {
		return edge.CredentialDocument{Heading: documentedHeading, Document: string(purpose)}, nil
	}
	return hooks
}

func TestPlanRemoveBootstrapNamesTheTierAndWhatGoes(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client, vendor := contractServed(t, "1.2.3")
	bootstrapped(t, vendor, environment.TierPreview, fake.PartCache)
	bootstrapOK(t, client, &contractv1.BootstrapRequest{
		Tier: environmentv1.Tier_TIER_PREVIEW,
		Edge: &contractv1.EdgeSelection{Kind: string(fake.KindDirect)},
	})

	plan, err := client.PlanRemoveBootstrap(ctx, &contractv1.BootstrapScope{Tier: environmentv1.Tier_TIER_PREVIEW})
	if err != nil {
		t.Fatalf("PlanRemoveBootstrap() error = %v", err)
	}
	if plan.GetSubject() != string(environment.TierPreview) {
		t.Errorf("subject = %q, want the tier the CLI asks the user to type back", plan.GetSubject())
	}
	if len(plan.GetGroups()) != 3 {
		t.Fatalf("PlanRemoveBootstrap() planned %d items, want the part stack, the core and the edge", len(plan.GetGroups()))
	}
	for _, item := range plan.GetGroups() {
		if item.GetAction() != planv1.Change_ACTION_DELETE {
			t.Errorf("item %s planned action %v, want it deleted", item.GetName(), item.GetAction())
		}
		if item.GetKind() == "" || item.GetName() == "" {
			t.Errorf("item %+v does not say what it is", item)
		}
	}
}

func TestRemoveBootstrapWillNotRemoveOneStillInUse(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client, vendor := contractServed(t, "1.2.3")
	bootstrapOK(t, client, &contractv1.BootstrapRequest{Tier: environmentv1.Tier_TIER_PRODUCTION})
	recordProject(t, vendor, "shop")

	_, err := client.PlanRemoveBootstrap(ctx, &contractv1.BootstrapScope{Tier: environmentv1.Tier_TIER_PRODUCTION})
	if got := connect.CodeOf(err); got != connect.CodeFailedPrecondition {
		t.Fatalf("PlanRemoveBootstrap() over an occupied bootstrap: code = %v, want %v (%v)", got, connect.CodeFailedPrecondition, err)
	}

	stream, err := client.RemoveBootstrap(ctx, &contractv1.BootstrapScope{Tier: environmentv1.Tier_TIER_PRODUCTION})
	if err != nil {
		t.Fatalf("RemoveBootstrap() error = %v", err)
	}
	result, err := drain(stream)
	if err != nil {
		t.Fatalf("RemoveBootstrap() stream = %v", err)
	}
	if result.GetSuccess() {
		t.Fatal("RemoveBootstrap() removed a bootstrap a project is still deployed into")
	}
	if !strings.Contains(result.GetError(), "shop") {
		t.Errorf("RemoveBootstrap() refused with %q, want it to name what is still there", result.GetError())
	}
}

func TestRemoveBootstrapTakesTheBootstrap(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client, _ := contractServed(t, "1.2.3")
	bootstrapOK(t, client, &contractv1.BootstrapRequest{
		Tier: environmentv1.Tier_TIER_PRODUCTION,
	})

	stream, err := client.RemoveBootstrap(ctx, &contractv1.BootstrapScope{Tier: environmentv1.Tier_TIER_PRODUCTION})
	if err != nil {
		t.Fatalf("RemoveBootstrap() error = %v", err)
	}
	result, err := drain(stream)
	if err != nil {
		t.Fatalf("RemoveBootstrap() stream = %v", err)
	}
	if result == nil || !result.GetSuccess() {
		t.Fatalf("RemoveBootstrap() result error = %q, want it to succeed", result.GetError())
	}

	planned, err := client.DescribeBootstrap(ctx, &contractv1.DescribeBootstrapRequest{Tier: environmentv1.Tier_TIER_PRODUCTION})
	if err != nil {
		t.Fatalf("DescribeBootstrap() error = %v", err)
	}
	if planned.GetBootstrap().GetPresent() {
		t.Error("DescribeBootstrap() still reports a bootstrap after it was removed")
	}
}

func TestRemoveBootstrapTearsDownTheEdgeItWasAsked(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client, vendor := contractServed(t, "1.2.3")
	bootstrapOK(t, client, &contractv1.BootstrapRequest{Tier: environmentv1.Tier_TIER_PRODUCTION})

	stream, err := client.RemoveBootstrap(ctx, &contractv1.BootstrapScope{
		Tier: environmentv1.Tier_TIER_PRODUCTION,
		Edge: &contractv1.EdgeSelection{Kind: string(fake.KindDirect)},
	})
	if err != nil {
		t.Fatalf("RemoveBootstrap() error = %v", err)
	}
	result, err := drain(stream)
	if err != nil {
		t.Fatalf("RemoveBootstrap() stream = %v", err)
	}
	if result == nil || !result.GetSuccess() {
		t.Fatalf("RemoveBootstrap() result error = %q, want it to succeed", result.GetError())
	}
	if fronting := vendor.FakeBootstrap().DefaultEdge(); fronting != fake.KindDirect {
		t.Errorf("RemoveBootstrap() removed the %q edge, want the %q the request named", fronting, fake.KindDirect)
	}
}

func TestPlanRemoveBootstrapPlansTheEdgeItWasAsked(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client, vendor := contractServed(t, "1.2.3")
	bootstrapOK(t, client, &contractv1.BootstrapRequest{Tier: environmentv1.Tier_TIER_PRODUCTION})

	plan, err := client.PlanRemoveBootstrap(ctx, &contractv1.BootstrapScope{
		Tier: environmentv1.Tier_TIER_PRODUCTION,
		Edge: &contractv1.EdgeSelection{Kind: string(fake.KindDirect)},
	})
	if err != nil {
		t.Fatalf("PlanRemoveBootstrap() error = %v", err)
	}
	if plan.GetEdgeKind() != string(fake.KindDirect) {
		t.Errorf("PlanRemoveBootstrap() planned against %q, want %q", plan.GetEdgeKind(), fake.KindDirect)
	}
	if fronting := vendor.FakeBootstrap().DefaultEdge(); fronting != fake.KindDirect {
		t.Errorf("PlanRemoveBootstrap() asked the provider for the %q edge, want the %q the request named", fronting, fake.KindDirect)
	}
}

func TestPlanRemoveBootstrapDropsTheEdgePhraseWhenMoreThanOneEdgeIsRaised(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client, vendor := contractServed(t, "1.2.3")
	bootstrapOK(t, client, &contractv1.BootstrapRequest{Tier: environmentv1.Tier_TIER_PRODUCTION})
	vendor.FakeBootstrap().SetRaisedEdges(fake.KindRelay, fake.KindDirect)

	plan, err := client.PlanRemoveBootstrap(ctx, &contractv1.BootstrapScope{Tier: environmentv1.Tier_TIER_PRODUCTION})
	if err != nil {
		t.Fatalf("PlanRemoveBootstrap() error = %v", err)
	}
	if plan.GetEdgeKind() != "" {
		t.Errorf("PlanRemoveBootstrap() says this account is fronted by the %q edge, want no single edge named where two are raised", plan.GetEdgeKind())
	}
	for _, kind := range []edge.Kind{fake.KindRelay, fake.KindDirect} {
		if !slices.ContainsFunc(plan.GetGroups(), func(g *planv1.ChangeGroup) bool {
			return g.GetName() == string(kind)+"/edge"
		}) {
			t.Errorf("plan groups = %+v, want a group named for the %q edge that is raised", plan.GetGroups(), kind)
		}
	}
}

func TestPlanRemoveBootstrapDropsTheEdgePhraseWhenNoEdgeIsRaised(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client, vendor := contractServed(t, "1.2.3")
	bootstrapOK(t, client, &contractv1.BootstrapRequest{Tier: environmentv1.Tier_TIER_PRODUCTION})
	vendor.FakeBootstrap().SetRaisedEdges()

	plan, err := client.PlanRemoveBootstrap(ctx, &contractv1.BootstrapScope{Tier: environmentv1.Tier_TIER_PRODUCTION})
	if err != nil {
		t.Fatalf("PlanRemoveBootstrap() error = %v", err)
	}
	if plan.GetEdgeKind() != "" {
		t.Errorf("PlanRemoveBootstrap() says this account is fronted by the %q edge, want none named where none is raised", plan.GetEdgeKind())
	}
}

func TestBootstrapRaisesTheEdgeTheProjectSelected(t *testing.T) {
	t.Parallel()

	client, vendor := contractServed(t, "1.2.3")

	bootstrapOK(t, client, &contractv1.BootstrapRequest{
		Tier: environmentv1.Tier_TIER_PRODUCTION,
		Edge: &contractv1.EdgeSelection{Kind: string(fake.KindDirect)},
	})
	if fronting := vendor.FakeBootstrap().DefaultEdge(); fronting != fake.KindDirect {
		t.Errorf("Bootstrap() raised the %q edge, want the %q this project selected", fronting, fake.KindDirect)
	}

	plan := streamedPlan(t, client, &contractv1.BootstrapRequest{
		Tier: environmentv1.Tier_TIER_PRODUCTION,
		Edge: &contractv1.EdgeSelection{Kind: string(fake.KindDirect)},
		Dry:  true,
	})
	if plan.GetEdgeKind() != string(fake.KindDirect) {
		t.Errorf("the plan was drawn against %q, want the %q this project selected", plan.GetEdgeKind(), fake.KindDirect)
	}
	if fronting := vendor.FakeBootstrap().DefaultEdge(); fronting != fake.KindDirect {
		t.Errorf("planning asked the provider for the %q edge, want the %q this project selected", fronting, fake.KindDirect)
	}
}

func TestBootstrapRefusesAnEdgeTheProviderDoesNotServe(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client, _ := contractServed(t, "1.2.3")
	bootstrapOK(t, client, &contractv1.BootstrapRequest{Tier: environmentv1.Tier_TIER_PRODUCTION})

	_, err := client.PlanRemoveBootstrap(ctx, &contractv1.BootstrapScope{
		Tier: environmentv1.Tier_TIER_PRODUCTION,
		Edge: &contractv1.EdgeSelection{Kind: "nowhere"},
	})
	if err == nil || !strings.Contains(err.Error(), "nowhere") {
		t.Fatalf("PlanRemoveBootstrap() against an unserved edge = %v, want it named in a refusal", err)
	}
}

func TestBootstrapDescribesAndCreatesATierWhoseStoreTheBootstrapCreates(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	vendor := vendorWithAStoreTheBootstrapCreates{fake.NewProvider(fake.Options{Region: "nowhere"}).WithProjectDir(workingDir(t))}
	client := servedProvider(t, "1.2.3", vendor)

	described, err := client.DescribeBootstrap(ctx, &contractv1.DescribeBootstrapRequest{
		Tier: environmentv1.Tier_TIER_PRODUCTION,
	})
	if err != nil {
		t.Fatalf("DescribeBootstrap() error = %v, want the bootstrap described as absent", err)
	}
	if described.GetBootstrap().GetPresent() {
		t.Fatal("DescribeBootstrap() reports a bootstrap where none was applied")
	}

	bootstrapOK(t, client, &contractv1.BootstrapRequest{Tier: environmentv1.Tier_TIER_PRODUCTION})
}

const deployAddedPart = "optimizer"

func withADeployAddedPart(t *testing.T, vendor *fake.Provider, users ...string) {
	t.Helper()
	bootstrap := vendor.FakeBootstrap()
	bootstrap.Offers(append(bootstrap.Parts(), provider.Part{Name: deployAddedPart, Summary: "a part the first deploy that uses it adds"})...)
	if users == nil {
		users = []string{}
	}
	bootstrap.AddPart(environment.TierProduction, provider.BootstrapPart{
		Name:      deployAddedPart,
		Resources: []provider.PartResource{{Kind: "Fake::Function", ID: "optimizer"}},
		UsedBy:    users,
	})
}

func TestARemovalOfAPartADeployAddedReachesTheVendor(t *testing.T) {
	t.Parallel()

	client, vendor := contractServed(t, "1.2.3")
	bootstrapOK(t, client, &contractv1.BootstrapRequest{Tier: environmentv1.Tier_TIER_PRODUCTION})
	withADeployAddedPart(t, vendor)

	bootstrapOK(t, client, &contractv1.BootstrapRequest{
		Tier:   environmentv1.Tier_TIER_PRODUCTION,
		Remove: []string{deployAddedPart},
	})

	applied := vendor.FakeBootstrap().Applied()
	if removing := applied[len(applied)-1]; !slices.Equal(removing.Remove, []string{deployAddedPart}) {
		t.Errorf("Apply() removed %v, want %s, a part a deploy added and no project uses", removing.Remove, deployAddedPart)
	}
	if _, listed := describedParts(t, client)[deployAddedPart]; listed {
		t.Errorf("%s is still described after its removal", deployAddedPart)
	}
}

func TestARemovalOfAPartAProjectUsesIsRefusedBeforeAnyPlanUnlessForced(t *testing.T) {
	t.Parallel()

	client, vendor := contractServed(t, "1.2.3")
	bootstrapOK(t, client, &contractv1.BootstrapRequest{Tier: environmentv1.Tier_TIER_PRODUCTION})
	withADeployAddedPart(t, vendor, "shop")
	before := len(vendor.FakeBootstrap().Applied())

	events, err := streamedEvents(t, client, &contractv1.BootstrapRequest{
		Tier:   environmentv1.Tier_TIER_PRODUCTION,
		Remove: []string{deployAddedPart},
		Dry:    true,
	})
	said := connectMessage(err)
	for _, event := range events {
		if event.GetPlan() != nil {
			t.Fatal("a dry removal of a part shop uses drew a plan, want it refused before the plan is shown")
		}
		said += event.GetResult().GetError()
	}
	for _, want := range []string{deployAddedPart, "shop", "--force"} {
		if !strings.Contains(said, want) {
			t.Errorf("the refusal reads %q, want it to contain %q", said, want)
		}
	}
	if got := len(vendor.FakeBootstrap().Applied()); got != before {
		t.Errorf("the refused removal applied the bootstrap %d times, want none", got-before)
	}

	plan := streamedPlan(t, client, &contractv1.BootstrapRequest{
		Tier:   environmentv1.Tier_TIER_PRODUCTION,
		Remove: []string{deployAddedPart},
		Force:  true,
		Dry:    true,
	})
	at := slices.IndexFunc(plan.GetGroups(), func(group *planv1.ChangeGroup) bool { return group.GetPart() == deployAddedPart })
	if at < 0 {
		t.Fatalf("a forced removal planned %v, want a group taking %s down", plan.GetGroups(), deployAddedPart)
	}
	if group := plan.GetGroups()[at]; group.GetAction() != planv1.Change_ACTION_DELETE || !strings.Contains(group.GetReason(), "shop") {
		t.Errorf("%s is planned %s %q, want it deleted with shop named as the project it breaks", deployAddedPart, group.GetAction(), group.GetReason())
	}

	bootstrapOK(t, client, &contractv1.BootstrapRequest{Tier: environmentv1.Tier_TIER_PRODUCTION, Remove: []string{deployAddedPart}, Force: true})
	applied := vendor.FakeBootstrap().Applied()
	if last := applied[len(applied)-1]; !last.Force || !slices.Contains(last.Remove, deployAddedPart) {
		t.Errorf("a forced removal reached the vendor as %+v, want the force passed on, so a vendor check that only restates who uses the part stands aside", last)
	}
}
