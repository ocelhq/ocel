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

	"github.com/ocelhq/ocel/pkg/channel"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/providerkit/fake"
)

func contractServed(t *testing.T, version string) (contractv1connect.ProviderServiceClient, *fake.Provider) {
	t.Helper()

	provider := fake.NewProvider(fake.Options{Region: "nowhere"})
	spec := providerkit.Spec{
		Version: version,
		New: func(context.Context, providerkit.Options) (providerkit.Provider, error) {
			return provider, nil
		},
	}
	const token = "a-token"
	server := httptest.NewServer(providerkit.NewMux(spec, token))
	t.Cleanup(server.Close)

	client := contractv1connect.NewProviderServiceClient(server.Client(), server.URL, connect.WithInterceptors(bearer(token)))
	if _, err := client.Configure(context.Background(), &contractv1.ConfigureRequest{}); err != nil {
		t.Fatalf("Configure() error = %v", err)
	}
	return client, provider
}

type bearer string

func (b bearer) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		req.Header().Set("Authorization", channel.FormatAuthHeader(string(b)))
		return next(ctx, req)
	}
}

func (b bearer) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return func(ctx context.Context, spec connect.Spec) connect.StreamingClientConn {
		conn := next(ctx, spec)
		conn.RequestHeader().Set("Authorization", channel.FormatAuthHeader(string(b)))
		return conn
	}
}

func (bearer) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return next
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

	planned, err := client.PlanBootstrap(ctx, &contractv1.PlanBootstrapRequest{Tier: environmentv1.Tier_TIER_PRODUCTION})
	if err != nil {
		t.Fatalf("PlanBootstrap() error = %v", err)
	}
	if !planned.GetBootstrap().GetAutoHeal() {
		t.Error("PlanBootstrap() reports auto_heal off after a bootstrap that turned it on")
	}

	bootstrapOK(t, client, &contractv1.BootstrapRequest{
		Tier:     environmentv1.Tier_TIER_PRODUCTION,
		Features: []string{fake.FeatureCache},
	})
	planned, err = client.PlanBootstrap(ctx, &contractv1.PlanBootstrapRequest{Tier: environmentv1.Tier_TIER_PRODUCTION})
	if err != nil {
		t.Fatalf("PlanBootstrap() error = %v", err)
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

func TestPlanBootstrapAnswersTheCatalogueAndTheStanding(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client, provider := contractServed(t, "1.2.3")
	bootstrapOK(t, client, &contractv1.BootstrapRequest{
		Tier:     environmentv1.Tier_TIER_PRODUCTION,
		Features: []string{fake.FeatureCache},
		Edge:     &contractv1.EdgeSelection{Kind: string(fake.KindDirect)},
	})
	recordProject(t, provider, "shop", fake.FeatureCache)

	planned, err := client.PlanBootstrap(ctx, &contractv1.PlanBootstrapRequest{
		Tier:           environmentv1.Tier_TIER_PRODUCTION,
		WithDependents: true,
		Edge:           &contractv1.EdgeSelection{Kind: string(fake.KindDirect)},
	})
	if err != nil {
		t.Fatalf("PlanBootstrap() error = %v", err)
	}

	features := planned.GetFeatures()
	if len(features) != 2 {
		t.Fatalf("PlanBootstrap() offered %d features, want the whole catalogue", len(features))
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
		t.Errorf("PlanBootstrap() status = %+v, want it present at schema %d", status, providerkit.BootstrapSchema)
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

func TestPlanBootstrapPlansTheApplyItsIntentNames(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client, _ := contractServed(t, "1.2.3")

	bare, err := client.PlanBootstrap(ctx, &contractv1.PlanBootstrapRequest{Tier: environmentv1.Tier_TIER_PRODUCTION})
	if err != nil {
		t.Fatalf("PlanBootstrap() error = %v", err)
	}
	if bare.GetPlan() != nil {
		t.Errorf("PlanBootstrap() drew a change plan for a request naming no intent: %v", bare.GetPlan())
	}

	planned, err := client.PlanBootstrap(ctx, &contractv1.PlanBootstrapRequest{
		Tier: environmentv1.Tier_TIER_PRODUCTION,
		Intent: &contractv1.BootstrapRequest{
			Tier:     environmentv1.Tier_TIER_PRODUCTION,
			Features: []string{fake.FeatureImages},
		},
	})
	if err != nil {
		t.Fatalf("PlanBootstrap() with an intent error = %v", err)
	}
	plan := planned.GetPlan()
	if plan.GetSubject() != string(providerkit.ClassProduction) {
		t.Errorf("plan subject = %q, want the class it was asked about", plan.GetSubject())
	}
	if plan.GetEdgeKind() != string(fake.KindRelay) {
		t.Errorf("plan was drawn against the %q edge, want the default the apply would bootstrap", plan.GetEdgeKind())
	}
	if len(plan.GetGroups()) != 3 {
		t.Fatalf("plan = %v, want the baseline and the closure of images", plan.GetGroups())
	}
	for _, group := range plan.GetGroups() {
		if group.GetAction() != contractv1.Change_ACTION_CREATE {
			t.Errorf("%s is %s, want it created on an account holding nothing", group.GetName(), group.GetAction())
		}
		if len(group.GetChanges()) == 0 {
			t.Errorf("%s carries no resource-level detail", group.GetName())
		}
	}
}

func TestPlanBootstrapRefusesAnIntentAimedElsewhere(t *testing.T) {
	t.Parallel()

	client, _ := contractServed(t, "1.2.3")

	_, err := client.PlanBootstrap(context.Background(), &contractv1.PlanBootstrapRequest{
		Tier: environmentv1.Tier_TIER_PRODUCTION,
		Intent: &contractv1.BootstrapRequest{
			Tier:     environmentv1.Tier_TIER_PREVIEW,
			Features: []string{fake.FeatureCache},
		},
	})
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("PlanBootstrap() with an intent aimed at another tier = %v, want it refused rather than silently planned against production", err)
	}
}

func TestPlanBootstrapRefusesAnIntentAimedAtAnotherEdge(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		asked   *contractv1.EdgeSelection
		aimed   *contractv1.EdgeSelection
		refused bool
	}{
		{
			name:    "the intent names an edge the question did not",
			aimed:   &contractv1.EdgeSelection{Kind: string(fake.KindRelay)},
			refused: true,
		},
		{
			name:    "the question names an edge the intent did not",
			asked:   &contractv1.EdgeSelection{Kind: string(fake.KindRelay)},
			refused: true,
		},
		{
			name:  "both name the same edge",
			asked: &contractv1.EdgeSelection{Kind: string(fake.KindRelay)},
			aimed: &contractv1.EdgeSelection{Kind: string(fake.KindRelay)},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			client, _ := contractServed(t, "1.2.3")
			_, err := client.PlanBootstrap(context.Background(), &contractv1.PlanBootstrapRequest{
				Tier: environmentv1.Tier_TIER_PRODUCTION,
				Edge: tc.asked,
				Intent: &contractv1.BootstrapRequest{
					Tier:     environmentv1.Tier_TIER_PRODUCTION,
					Features: []string{fake.FeatureCache},
					Edge:     tc.aimed,
				},
			})
			if tc.refused && connect.CodeOf(err) != connect.CodeInvalidArgument {
				t.Fatalf("PlanBootstrap() = %v, want an intent aimed at another edge refused rather than planned against this one", err)
			}
			if !tc.refused && err != nil {
				t.Fatalf("PlanBootstrap() error = %v, want a plan where question and intent name one edge", err)
			}
		})
	}
}

func TestPlanBootstrapReportsADowngrade(t *testing.T) {
	t.Parallel()

	client, provider := contractServed(t, "1.0.0")
	bootstrapOK(t, client, &contractv1.BootstrapRequest{Tier: environmentv1.Tier_TIER_PRODUCTION})
	provider.Bootstrapper().WrittenBy("2.0.0")

	planned, err := client.PlanBootstrap(context.Background(), &contractv1.PlanBootstrapRequest{
		Tier: environmentv1.Tier_TIER_PRODUCTION,
	})
	if err != nil {
		t.Fatalf("PlanBootstrap() error = %v", err)
	}
	if !planned.GetBootstrap().GetDowngrade() {
		t.Error("PlanBootstrap() reports no downgrade where a newer build wrote the bootstrap")
	}
}

func TestGetCredentialPolicyRendersEitherTier(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client, _ := contractServed(t, "1.2.3")

	for tier, want := range map[contractv1.CredentialTier]string{
		contractv1.CredentialTier_CREDENTIAL_TIER_BOOTSTRAP: string(providerkit.TierBootstrap),
		contractv1.CredentialTier_CREDENTIAL_TIER_DEPLOY:    string(providerkit.TierDeploy),
	} {
		policy, err := client.GetCredentialPolicy(ctx, &contractv1.CredentialPolicyRequest{Tier: tier})
		if err != nil {
			t.Fatalf("GetCredentialPolicy(%v) error = %v", tier, err)
		}
		if !strings.Contains(policy.GetDocument(), want) {
			t.Errorf("GetCredentialPolicy(%v) = %q, want it to render the %s tier", tier, policy.GetDocument(), want)
		}
	}

	_, err := client.GetCredentialPolicy(ctx, &contractv1.CredentialPolicyRequest{})
	if got := connect.CodeOf(err); got != connect.CodeInvalidArgument {
		t.Fatalf("GetCredentialPolicy() naming no tier: code = %v, want %v", got, connect.CodeInvalidArgument)
	}
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
	if len(plan.GetGroups()) != 2 {
		t.Fatalf("PlanRemoveBootstrap() planned %d items, want the feature stack and the core", len(plan.GetGroups()))
	}
	for _, item := range plan.GetGroups() {
		if item.GetAction() != contractv1.Change_ACTION_DELETE {
			t.Errorf("item %s planned action %v, want it deleted", item.GetName(), item.GetAction())
		}
		if item.GetKind() == "" || item.GetName() == "" || item.GetReason() == "" {
			t.Errorf("item %+v does not say what it is or why it goes", item)
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

	planned, err := client.PlanBootstrap(ctx, &contractv1.PlanBootstrapRequest{Tier: environmentv1.Tier_TIER_PRODUCTION})
	if err != nil {
		t.Fatalf("PlanBootstrap() error = %v", err)
	}
	if planned.GetBootstrap().GetPresent() {
		t.Error("PlanBootstrap() still reports a bootstrap after it was removed")
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

func TestBootstrapStandsUpTheEdgeTheProjectSelected(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client, provider := contractServed(t, "1.2.3")

	bootstrapOK(t, client, &contractv1.BootstrapRequest{
		Tier: environmentv1.Tier_TIER_PRODUCTION,
		Edge: &contractv1.EdgeSelection{Kind: string(fake.KindDirect)},
	})
	if fronting := provider.Bootstrapper().Fronting(); fronting != fake.KindDirect {
		t.Errorf("Bootstrap() stood up the %q edge, want the %q this project selected", fronting, fake.KindDirect)
	}

	plan, err := client.PlanBootstrap(ctx, &contractv1.PlanBootstrapRequest{
		Tier: environmentv1.Tier_TIER_PRODUCTION,
		Edge: &contractv1.EdgeSelection{Kind: string(fake.KindDirect)},
		Intent: &contractv1.BootstrapRequest{
			Tier: environmentv1.Tier_TIER_PRODUCTION,
			Edge: &contractv1.EdgeSelection{Kind: string(fake.KindDirect)},
		},
	})
	if err != nil {
		t.Fatalf("PlanBootstrap() error = %v", err)
	}
	if plan.GetPlan().GetEdgeKind() != string(fake.KindDirect) {
		t.Errorf("PlanBootstrap() planned against %q, want the %q this project selected", plan.GetPlan().GetEdgeKind(), fake.KindDirect)
	}
	if fronting := provider.Bootstrapper().Fronting(); fronting != fake.KindDirect {
		t.Errorf("PlanBootstrap() asked the provider for the %q edge, want the %q this project selected", fronting, fake.KindDirect)
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
