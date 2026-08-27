package providerkit_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	connect "connectrpc.com/connect"

	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	planv1 "github.com/ocelhq/ocel/pkg/proto/common/plan/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/providerkit/fake"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func contractServed(t *testing.T, version string) (contractv1connect.ProviderServiceClient, *fake.Provider) {
	t.Helper()

	provider := fake.NewProvider(fake.Options{Region: "nowhere"})
	return servedProvider(t, version, provider), provider
}

func servedProvider(t *testing.T, version string, provider providerkit.Provider) contractv1connect.ProviderServiceClient {
	t.Helper()

	spec := providerkit.Spec{
		Version: version,
		New: func(context.Context, providerkit.Options) (providerkit.Provider, error) {
			return provider, nil
		},
	}
	server := httptest.NewServer(providerkit.ConformanceMux(spec))
	t.Cleanup(server.Close)

	client := contractv1connect.NewProviderServiceClient(server.Client(), server.URL)
	if _, err := client.Configure(context.Background(), &contractv1.ConfigureRequest{}); err != nil {
		t.Fatalf("Configure() error = %v", err)
	}
	return client
}

func drain(stream *connect.ServerStreamForClient[progressv1.OperationEvent]) (*progressv1.ResultEvent, error) {
	defer stream.Close()
	var result *progressv1.ResultEvent
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
		t.Fatalf("Bootstrap() result = %v, want it to succeed", result)
	}
}

func TestBootstrapPullsInWhatAFeatureDependsOn(t *testing.T) {
	t.Parallel()

	client, provider := contractServed(t, "1.2.3")
	bootstrapOK(t, client, &contractv1.BootstrapRequest{
		Tier:     environmentv1.Tier_TIER_PRODUCTION,
		Features: []string{fake.FeatureImages},
	})

	applied := provider.Bootstrapper().Applied()
	if len(applied) != 1 {
		t.Fatalf("Apply() ran %d times, want once", len(applied))
	}
	if want := []string{fake.FeatureCache, fake.FeatureImages}; !slices.Equal(applied[0].Features, want) {
		t.Errorf("Apply() was asked for %v, want %v", applied[0].Features, want)
	}
	if applied[0].Class != providerkit.ClassProduction {
		t.Errorf("Apply() ran against %s, want %s", applied[0].Class, providerkit.ClassProduction)
	}
	if !applied[0].Unattended {
		t.Error("Apply() ran attended where nothing accepted replacements")
	}
}

func TestBootstrapAcceptsReplacementsWhenTheRequestDoes(t *testing.T) {
	t.Parallel()

	client, provider := contractServed(t, "1.2.3")
	bootstrapOK(t, client, &contractv1.BootstrapRequest{
		Tier:               environmentv1.Tier_TIER_PRODUCTION,
		Features:           []string{fake.FeatureCache},
		AcceptReplacements: true,
	})

	applied := provider.Bootstrapper().Applied()
	if applied[0].Unattended {
		t.Error("Apply() ran unattended where the request accepted replacements")
	}
}

func TestBootstrapRefusesAFeatureThisProviderDoesNotOffer(t *testing.T) {
	t.Parallel()

	client, _ := contractServed(t, "1.2.3")
	stream, err := client.Bootstrap(context.Background(), &contractv1.BootstrapRequest{
		Tier:     environmentv1.Tier_TIER_PRODUCTION,
		Features: []string{"quantum-edge"},
	})
	if err == nil {
		_, err = drain(stream)
	}
	if got := connect.CodeOf(err); got != connect.CodeInvalidArgument {
		t.Fatalf("Bootstrap() with an unknown feature: code = %v, want %v (%v)", got, connect.CodeInvalidArgument, err)
	}
	if !strings.Contains(err.Error(), fake.FeatureCache) {
		t.Errorf("Bootstrap() = %v, want it to name what this provider does offer", err)
	}
}

func TestBootstrapRecordsAutoHealAndTheRecordSchema(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client, provider := contractServed(t, "1.2.3")
	healing := true
	bootstrapOK(t, client, &contractv1.BootstrapRequest{
		Tier:     environmentv1.Tier_TIER_PRODUCTION,
		Features: []string{fake.FeatureCache},
		AutoHeal: &healing,
	})

	held, err := provider.Records().Read(ctx, providerkit.BootstrapRecord(providerkit.ClassProduction))
	if err != nil {
		t.Fatalf("Read() of the bootstrap record = %v", err)
	}
	var state providerkit.BootstrapState
	if err := json.Unmarshal(held.Bytes, &state); err != nil || !state.AutoHeal {
		t.Fatalf("the bootstrap record holds %q, %v, want auto_heal on", held.Bytes, err)
	}

	written, err := providerkit.RecordSchema(ctx, provider.Records(), providerkit.ClassProduction)
	if err != nil || written != providerkit.RecordSchemaVersion {
		t.Fatalf("RecordSchema() = %d, %v, want the bootstrap to have stamped %d", written, err, providerkit.RecordSchemaVersion)
	}

	planned, err := client.DescribeBootstrap(ctx, &contractv1.DescribeBootstrapRequest{Tier: environmentv1.Tier_TIER_PRODUCTION})
	if err != nil {
		t.Fatalf("DescribeBootstrap() error = %v", err)
	}
	if !planned.GetBootstrap().GetAutoHeal() {
		t.Error("DescribeBootstrap() reports auto_heal off after a bootstrap that turned it on")
	}

	bootstrapOK(t, client, &contractv1.BootstrapRequest{
		Tier:     environmentv1.Tier_TIER_PRODUCTION,
		Features: []string{fake.FeatureCache},
	})
	planned, err = client.DescribeBootstrap(ctx, &contractv1.DescribeBootstrapRequest{Tier: environmentv1.Tier_TIER_PRODUCTION})
	if err != nil {
		t.Fatalf("DescribeBootstrap() error = %v", err)
	}
	if !planned.GetBootstrap().GetAutoHeal() {
		t.Error("a bootstrap that named no auto_heal turned the standing one off")
	}
}

func TestBootstrapRefusesToRemoveAFeatureAProjectRecorded(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client, provider := contractServed(t, "1.2.3")
	bootstrapOK(t, client, &contractv1.BootstrapRequest{
		Tier:     environmentv1.Tier_TIER_PRODUCTION,
		Features: []string{fake.FeatureImages},
	})
	recordProject(t, provider, "shop", fake.FeatureImages)

	stream, err := client.Bootstrap(ctx, &contractv1.BootstrapRequest{
		Tier:   environmentv1.Tier_TIER_PRODUCTION,
		Edge:   &contractv1.EdgeSelection{Kind: string(fake.KindDirect)},
		Remove: []string{fake.FeatureImages},
	})
	if err != nil {
		t.Fatalf("Bootstrap() error = %v", err)
	}
	result, err := drain(stream)
	if err != nil {
		t.Fatalf("Bootstrap() stream = %v", err)
	}
	if result.GetSuccess() {
		t.Fatal("Bootstrap() removed a feature a deployed project recorded")
	}
	for _, want := range []string{fake.FeatureImages, "shop", "--force"} {
		if !strings.Contains(result.GetError(), want) {
			t.Errorf("Bootstrap() refused with %q, want it to contain %q", result.GetError(), want)
		}
	}
}

func TestBootstrapRemovesInDeleteOrderWhenForced(t *testing.T) {
	t.Parallel()

	client, provider := contractServed(t, "1.2.3")
	bootstrapOK(t, client, &contractv1.BootstrapRequest{
		Tier:     environmentv1.Tier_TIER_PRODUCTION,
		Features: []string{fake.FeatureImages},
	})
	recordProject(t, provider, "shop", fake.FeatureImages)

	bootstrapOK(t, client, &contractv1.BootstrapRequest{
		Tier:   environmentv1.Tier_TIER_PRODUCTION,
		Edge:   &contractv1.EdgeSelection{Kind: string(fake.KindDirect)},
		Remove: []string{fake.FeatureCache},
		Force:  true,
	})

	applied := provider.Bootstrapper().Applied()
	removing := applied[len(applied)-1]
	if want := []string{fake.FeatureImages, fake.FeatureCache}; !slices.Equal(removing.Remove, want) {
		t.Errorf("Apply() removed %v, want %v — what stands on a feature goes first", removing.Remove, want)
	}
	if len(removing.Features) != 0 {
		t.Errorf("Apply() was asked for %v, want a run that named only removals to ensure nothing", removing.Features)
	}
}

func TestBootstrapLeavesAFeatureNoRunNamed(t *testing.T) {
	t.Parallel()

	client, provider := contractServed(t, "1.2.3")
	bootstrapOK(t, client, &contractv1.BootstrapRequest{
		Tier:     environmentv1.Tier_TIER_PRODUCTION,
		Features: []string{fake.FeatureImages},
	})
	bootstrapOK(t, client, &contractv1.BootstrapRequest{
		Tier:     environmentv1.Tier_TIER_PRODUCTION,
		Edge:     &contractv1.EdgeSelection{Kind: string(fake.KindDirect)},
		Features: []string{fake.FeatureCache},
	})

	applied := provider.Bootstrapper().Applied()
	last := applied[len(applied)-1]
	if len(last.Remove) != 0 {
		t.Errorf("Apply() removed %v, want a run naming only %s to leave the rest of the account alone",
			last.Remove, fake.FeatureCache)
	}
}

func recordProject(t *testing.T, provider *fake.Provider, slug string, features ...string) {
	t.Helper()

	body, err := json.Marshal(providerkit.Project{Features: features})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Records().Write(context.Background(), providerkit.Record{
		Name:  providerkit.ProjectRecord(providerkit.ClassProduction, slug),
		Bytes: body,
	}); err != nil {
		t.Fatal(err)
	}
}

func TestDescribeBootstrapAnswersTheCatalogueAndTheStanding(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client, provider := contractServed(t, "1.2.3")
	bootstrapOK(t, client, &contractv1.BootstrapRequest{
		Tier:     environmentv1.Tier_TIER_PRODUCTION,
		Features: []string{fake.FeatureCache},
		Edge:     &contractv1.EdgeSelection{Kind: string(fake.KindDirect)},
	})
	recordProject(t, provider, "shop", fake.FeatureCache)

	planned, err := client.DescribeBootstrap(ctx, &contractv1.DescribeBootstrapRequest{
		Tier:           environmentv1.Tier_TIER_PRODUCTION,
		WithDependents: true,
		Edge:           &contractv1.EdgeSelection{Kind: string(fake.KindDirect)},
	})
	if err != nil {
		t.Fatalf("DescribeBootstrap() error = %v", err)
	}

	features := planned.GetFeatures()
	if len(features) != 2 {
		t.Fatalf("DescribeBootstrap() offered %d features, want the whole catalogue", len(features))
	}
	if features[0].GetName() != fake.FeatureCache || !features[0].GetEnabled() {
		t.Errorf("%s = %+v, want it enabled", fake.FeatureCache, features[0])
	}
	if !slices.Equal(features[0].GetDependents(), []string{"shop"}) {
		t.Errorf("%s dependents = %v, want the project that recorded it", fake.FeatureCache, features[0].GetDependents())
	}
	if features[1].GetEnabled() {
		t.Errorf("%s = %+v, want it left out", fake.FeatureImages, features[1])
	}
	if !slices.Equal(features[1].GetDependsOn(), []string{fake.FeatureCache}) {
		t.Errorf("%s depends on %v, want %v", fake.FeatureImages, features[1].GetDependsOn(), fake.FeatureCache)
	}

	status := planned.GetBootstrap()
	if !status.GetPresent() || status.GetSchema() != providerkit.BootstrapSchema {
		t.Errorf("DescribeBootstrap() status = %+v, want it present at schema %d", status, providerkit.BootstrapSchema)
	}
	if status.GetRequiredSchema() != providerkit.BootstrapSchema {
		t.Errorf("required_schema = %d, want %d", status.GetRequiredSchema(), providerkit.BootstrapSchema)
	}
	if status.GetWriter() != "1.2.3" {
		t.Errorf("writer = %q, want the version this provider was built as", status.GetWriter())
	}
	for _, stack := range status.GetStacks() {
		if !stack.GetRequired() {
			t.Errorf("stack %s is not required, and everything standing was asked for", stack.GetName())
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

func TestAnApplyCarryingAConsentedPlanDrawsNoPlanOfItsOwn(t *testing.T) {
	t.Parallel()

	client, _ := contractServed(t, "1.2.3")
	req := &contractv1.BootstrapRequest{
		Tier:     environmentv1.Tier_TIER_PRODUCTION,
		Features: []string{fake.FeatureImages},
		Dry:      true,
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

	client, provider := contractServed(t, "1.2.3")
	bootstrapped(t, provider, providerkit.ClassProduction, fake.FeatureCache)

	req := &contractv1.BootstrapRequest{
		Tier:     environmentv1.Tier_TIER_PRODUCTION,
		Features: []string{fake.FeatureCache},
		Dry:      true,
	}
	consented := streamedPlan(t, client, req)
	if consented == nil {
		t.Fatal("the dry run streamed no plan, and the only thing consented to is the plan")
	}
	provider.Bootstrapper().Behind(fake.FeatureCache)

	req.Dry, req.Consented = false, consented
	_, err := streamedEvents(t, client, req)
	if err == nil {
		t.Fatal("Bootstrap() = nil, want an apply that outgrew its consented plan refused")
	}
	if !strings.Contains(err.Error(), fake.FeatureCache) {
		t.Errorf("the refusal reads %q, want it to name what moved under the plan", err)
	}
}

func TestAnApplyRefusesAConsentedPlanItCannotRead(t *testing.T) {
	t.Parallel()

	client, _ := contractServed(t, "1.2.3")
	unreadable := planv1.Change_Action(len(planv1.Change_Action_name) + 40)
	_, err := streamedEvents(t, client, &contractv1.BootstrapRequest{
		Tier:     environmentv1.Tier_TIER_PRODUCTION,
		Features: []string{fake.FeatureImages},
		Consented: &planv1.ChangePlan{Groups: []*planv1.ChangeGroup{
			{Kind: providerkit.StackGroupKind, Name: "core", Action: unreadable},
		}},
	})
	if err == nil {
		t.Fatal("Bootstrap() = nil, want an apply carrying an action this kit cannot read refused")
	}
	if !strings.Contains(err.Error(), unreadable.String()) {
		t.Errorf("the refusal reads %q, want it to name the action it could not read", err)
	}
}

func TestARemovalRefusesWorkTheConsentedPlanNeverShowed(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client, provider := contractServed(t, "1.2.3")
	bootstrapped(t, provider, providerkit.ClassProduction, fake.FeatureCache)

	scope := &contractv1.BootstrapScope{Tier: environmentv1.Tier_TIER_PRODUCTION}
	consented, err := client.PlanRemoveBootstrap(ctx, scope)
	if err != nil {
		t.Fatalf("PlanRemoveBootstrap() error = %v", err)
	}

	bootstrapped(t, provider, providerkit.ClassProduction, fake.FeatureCache, fake.FeatureImages)

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
	if !strings.Contains(stream.Err().Error(), fake.FeatureImages) {
		t.Errorf("the refusal reads %q, want it to name what stood up under the plan", stream.Err())
	}
}

func TestBootstrapShowsThePlanItIsAboutToApply(t *testing.T) {
	t.Parallel()

	client, provider := contractServed(t, "1.2.3")
	plan := streamedPlan(t, client, &contractv1.BootstrapRequest{
		Tier:     environmentv1.Tier_TIER_PRODUCTION,
		Features: []string{fake.FeatureImages},
	})
	if plan == nil {
		t.Fatal("Bootstrap() streamed no plan, and the only thing consented to is the plan")
	}
	if plan.GetSubject() != string(providerkit.ClassProduction) {
		t.Errorf("plan subject = %q, want the class it applies to", plan.GetSubject())
	}
	if plan.GetEdgeKind() != string(fake.KindRelay) {
		t.Errorf("plan was drawn against the %q edge, want the default the apply bootstraps", plan.GetEdgeKind())
	}
	if len(plan.GetGroups()) != 3 {
		t.Fatalf("plan = %v, want the baseline and the closure of images", plan.GetGroups())
	}
	for _, group := range plan.GetGroups() {
		if group.GetAction() != planv1.Change_ACTION_CREATE {
			t.Errorf("%s is %s, want it created on an account holding nothing", group.GetName(), group.GetAction())
		}
		if len(group.GetChanges()) == 0 {
			t.Errorf("%s carries no resource-level detail", group.GetName())
		}
	}
	if len(provider.Bootstrapper().Applied()) != 1 {
		t.Errorf("the bootstrapper was applied %d times, want the one this stream carried out",
			len(provider.Bootstrapper().Applied()))
	}
}

func TestADryBootstrapDrawsThePlanAndStandsNothingUp(t *testing.T) {
	t.Parallel()

	client, provider := contractServed(t, "1.2.3")
	plan := streamedPlan(t, client, &contractv1.BootstrapRequest{
		Tier:     environmentv1.Tier_TIER_PRODUCTION,
		Features: []string{fake.FeatureImages},
		Dry:      true,
	})
	if plan == nil || len(plan.GetGroups()) == 0 {
		t.Fatal("a dry bootstrap streamed no plan, and drawing the plan is all it is for")
	}
	if applied := provider.Bootstrapper().Applied(); len(applied) != 0 {
		t.Errorf("a dry bootstrap applied %v, want it to change nothing", applied)
	}
}

func TestDescribeBootstrapReportsADowngrade(t *testing.T) {
	t.Parallel()

	client, provider := contractServed(t, "1.0.0")
	bootstrapOK(t, client, &contractv1.BootstrapRequest{Tier: environmentv1.Tier_TIER_PRODUCTION})
	provider.Bootstrapper().WrittenBy("2.0.0")

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

func TestGetCredentialPermissionsRendersEitherTier(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client, _ := contractServed(t, "1.2.3")

	for tier, want := range map[contractv1.CredentialTier]string{
		contractv1.CredentialTier_CREDENTIAL_TIER_BOOTSTRAP: string(providerkit.TierBootstrap),
		contractv1.CredentialTier_CREDENTIAL_TIER_DEPLOY:    string(providerkit.TierDeploy),
	} {
		permissions, err := client.GetCredentialPermissions(ctx, &contractv1.CredentialPermissionsRequest{Tier: tier})
		if err != nil {
			t.Fatalf("GetCredentialPermissions(%v) error = %v", tier, err)
		}
		groups := permissions.GetGroups()
		if len(groups) != 1 {
			t.Fatalf("GetCredentialPermissions(%v) rendered %d groups, want only the vendor's", tier, len(groups))
		}
		if got := groups[0].GetHeading(); got != "fake credentials" {
			t.Errorf("GetCredentialPermissions(%v) heading = %q, want the vendor's own heading", tier, got)
		}
		if !strings.Contains(groups[0].GetDocument(), want) {
			t.Errorf("GetCredentialPermissions(%v) = %q, want it to render the %s tier", tier, groups[0].GetDocument(), want)
		}
	}

	_, err := client.GetCredentialPermissions(ctx, &contractv1.CredentialPermissionsRequest{})
	if got := connect.CodeOf(err); got != connect.CodeInvalidArgument {
		t.Fatalf("GetCredentialPermissions() naming no tier: code = %v, want %v", got, connect.CodeInvalidArgument)
	}
}

func TestGetCredentialPermissionsAppendsWhatTheEdgeDocuments(t *testing.T) {
	t.Parallel()

	provider := fake.NewProvider(fake.Options{Region: "nowhere"})
	client := servedProvider(t, "1.2.3", documentingProvider{Provider: provider})

	permissions, err := client.GetCredentialPermissions(context.Background(), &contractv1.CredentialPermissionsRequest{
		Tier: contractv1.CredentialTier_CREDENTIAL_TIER_DEPLOY,
		Edge: &contractv1.EdgeSelection{Kind: string(fake.KindDirect)},
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
	if got := groups[1].GetDocument(); got != string(edge.TierDeploy) {
		t.Errorf("GetCredentialPermissions() second document = %q, want the edge asked for the %s tier", got, edge.TierDeploy)
	}
}

const documentedHeading = "an edge token"

type documentingProvider struct {
	providerkit.Provider
}

func (p documentingProvider) Edges() providerkit.EdgeRegistry {
	return documentingEdges{EdgeRegistry: p.Provider.Edges()}
}

type documentingEdges struct {
	providerkit.EdgeRegistry
}

func (e documentingEdges) Open(kind edge.Kind) (edge.Edge, error) {
	front, err := e.EdgeRegistry.Open(kind)
	if err != nil {
		return nil, err
	}
	return documentingEdge{Edge: front}, nil
}

type documentingEdge struct {
	edge.Edge
}

func (documentingEdge) CredentialPermissions(tier edge.CredentialTier) (edge.CredentialDocument, error) {
	return edge.CredentialDocument{Heading: documentedHeading, Document: string(tier)}, nil
}

func TestPlanRemoveBootstrapNamesTheClassAndWhatGoes(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client, _ := contractServed(t, "1.2.3")
	bootstrapOK(t, client, &contractv1.BootstrapRequest{
		Tier:     environmentv1.Tier_TIER_PREVIEW,
		Features: []string{fake.FeatureCache},
		Edge:     &contractv1.EdgeSelection{Kind: string(fake.KindDirect)},
	})

	plan, err := client.PlanRemoveBootstrap(ctx, &contractv1.BootstrapScope{Tier: environmentv1.Tier_TIER_PREVIEW})
	if err != nil {
		t.Fatalf("PlanRemoveBootstrap() error = %v", err)
	}
	if plan.GetSubject() != string(providerkit.ClassPreview) {
		t.Errorf("subject = %q, want the class the CLI asks the user to type back", plan.GetSubject())
	}
	if len(plan.GetGroups()) != 3 {
		t.Fatalf("PlanRemoveBootstrap() planned %d items, want the feature stack, the core and the edge", len(plan.GetGroups()))
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
	client, provider := contractServed(t, "1.2.3")
	bootstrapOK(t, client, &contractv1.BootstrapRequest{Tier: environmentv1.Tier_TIER_PRODUCTION})
	recordProject(t, provider, "shop")

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

func TestRemoveBootstrapTakesTheBootstrapAndItsRecord(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client, provider := contractServed(t, "1.2.3")
	healing := true
	bootstrapOK(t, client, &contractv1.BootstrapRequest{
		Tier:     environmentv1.Tier_TIER_PRODUCTION,
		Features: []string{fake.FeatureCache},
		AutoHeal: &healing,
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
		t.Fatalf("RemoveBootstrap() result = %v, want it to succeed", result)
	}

	planned, err := client.DescribeBootstrap(ctx, &contractv1.DescribeBootstrapRequest{Tier: environmentv1.Tier_TIER_PRODUCTION})
	if err != nil {
		t.Fatalf("DescribeBootstrap() error = %v", err)
	}
	if planned.GetBootstrap().GetPresent() {
		t.Error("DescribeBootstrap() still reports a bootstrap after it was removed")
	}
	if _, err := provider.Records().Read(ctx, providerkit.BootstrapRecord(providerkit.ClassProduction)); !errors.Is(err, providerkit.ErrNoRecord) {
		t.Errorf("the bootstrap record survived the removal: %v", err)
	}
}

func TestRemoveBootstrapTearsDownTheEdgeItWasAsked(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client, provider := contractServed(t, "1.2.3")
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
		t.Fatalf("RemoveBootstrap() result = %v, want it to succeed", result)
	}
	if fronting := provider.Bootstrapper().Fronting(); fronting != fake.KindDirect {
		t.Errorf("RemoveBootstrap() removed the %q edge, want the %q the request named", fronting, fake.KindDirect)
	}
}

func TestPlanRemoveBootstrapPlansTheEdgeItWasAsked(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client, provider := contractServed(t, "1.2.3")
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
	if fronting := provider.Bootstrapper().Fronting(); fronting != fake.KindDirect {
		t.Errorf("PlanRemoveBootstrap() asked the provider for the %q edge, want the %q the request named", fronting, fake.KindDirect)
	}
}

func TestPlanRemoveBootstrapDropsTheEdgePhraseWhenMoreThanOneEdgeStands(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client, provider := contractServed(t, "1.2.3")
	bootstrapOK(t, client, &contractv1.BootstrapRequest{Tier: environmentv1.Tier_TIER_PRODUCTION})
	provider.Bootstrapper().Standing(fake.KindRelay, fake.KindDirect)

	plan, err := client.PlanRemoveBootstrap(ctx, &contractv1.BootstrapScope{Tier: environmentv1.Tier_TIER_PRODUCTION})
	if err != nil {
		t.Fatalf("PlanRemoveBootstrap() error = %v", err)
	}
	if plan.GetEdgeKind() != "" {
		t.Errorf("PlanRemoveBootstrap() says this account is fronted by the %q edge, want no single edge named where two stand", plan.GetEdgeKind())
	}
	for _, kind := range []edge.Kind{fake.KindRelay, fake.KindDirect} {
		if !slices.ContainsFunc(plan.GetGroups(), func(g *planv1.ChangeGroup) bool {
			return g.GetName() == string(kind)+"/edge"
		}) {
			t.Errorf("plan groups = %+v, want a group named for the %q edge that stands", plan.GetGroups(), kind)
		}
	}
}

func TestPlanRemoveBootstrapDropsTheEdgePhraseWhenNoEdgeStands(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client, provider := contractServed(t, "1.2.3")
	bootstrapOK(t, client, &contractv1.BootstrapRequest{Tier: environmentv1.Tier_TIER_PRODUCTION})
	provider.Bootstrapper().Standing()

	plan, err := client.PlanRemoveBootstrap(ctx, &contractv1.BootstrapScope{Tier: environmentv1.Tier_TIER_PRODUCTION})
	if err != nil {
		t.Fatalf("PlanRemoveBootstrap() error = %v", err)
	}
	if plan.GetEdgeKind() != "" {
		t.Errorf("PlanRemoveBootstrap() says this account is fronted by the %q edge, want none named where none stands", plan.GetEdgeKind())
	}
}

func TestBootstrapStandsUpTheEdgeTheProjectSelected(t *testing.T) {
	t.Parallel()

	client, provider := contractServed(t, "1.2.3")

	bootstrapOK(t, client, &contractv1.BootstrapRequest{
		Tier: environmentv1.Tier_TIER_PRODUCTION,
		Edge: &contractv1.EdgeSelection{Kind: string(fake.KindDirect)},
	})
	if fronting := provider.Bootstrapper().Fronting(); fronting != fake.KindDirect {
		t.Errorf("Bootstrap() stood up the %q edge, want the %q this project selected", fronting, fake.KindDirect)
	}

	plan := streamedPlan(t, client, &contractv1.BootstrapRequest{
		Tier: environmentv1.Tier_TIER_PRODUCTION,
		Edge: &contractv1.EdgeSelection{Kind: string(fake.KindDirect)},
		Dry:  true,
	})
	if plan.GetEdgeKind() != string(fake.KindDirect) {
		t.Errorf("the plan was drawn against %q, want the %q this project selected", plan.GetEdgeKind(), fake.KindDirect)
	}
	if fronting := provider.Bootstrapper().Fronting(); fronting != fake.KindDirect {
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
