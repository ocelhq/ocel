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

	applied := provider.Bootstrap().(*fake.Bootstrapper).Applied()
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

	applied := provider.Bootstrap().(*fake.Bootstrapper).Applied()
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

	described, err := client.DescribeBootstrap(ctx, &contractv1.DescribeBootstrapRequest{Tier: environmentv1.Tier_TIER_PRODUCTION})
	if err != nil {
		t.Fatalf("DescribeBootstrap() error = %v", err)
	}
	if !described.GetBootstrap().GetAutoHeal() {
		t.Error("DescribeBootstrap() reports auto_heal off after a bootstrap that turned it on")
	}

	bootstrapOK(t, client, &contractv1.BootstrapRequest{
		Tier:     environmentv1.Tier_TIER_PRODUCTION,
		Features: []string{fake.FeatureCache},
	})
	described, err = client.DescribeBootstrap(ctx, &contractv1.DescribeBootstrapRequest{Tier: environmentv1.Tier_TIER_PRODUCTION})
	if err != nil {
		t.Fatalf("DescribeBootstrap() error = %v", err)
	}
	if !described.GetBootstrap().GetAutoHeal() {
		t.Error("a bootstrap that named no auto_heal turned the standing one off")
	}
}

func TestBootstrapRefusesToDropAFeatureAProjectRecorded(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client, provider := contractServed(t, "1.2.3")
	bootstrapOK(t, client, &contractv1.BootstrapRequest{
		Tier:     environmentv1.Tier_TIER_PRODUCTION,
		Features: []string{fake.FeatureImages},
	})
	recordProject(t, provider, "shop", fake.FeatureImages)

	stream, err := client.Bootstrap(ctx, &contractv1.BootstrapRequest{
		Tier:     environmentv1.Tier_TIER_PRODUCTION,
		Features: []string{fake.FeatureCache},
	})
	if err != nil {
		t.Fatalf("Bootstrap() error = %v", err)
	}
	result, err := drain(stream)
	if err != nil {
		t.Fatalf("Bootstrap() stream = %v", err)
	}
	if result.GetSuccess() {
		t.Fatal("Bootstrap() dropped a feature a deployed project recorded")
	}
	for _, want := range []string{fake.FeatureImages, "shop", "--force"} {
		if !strings.Contains(result.GetError(), want) {
			t.Errorf("Bootstrap() refused with %q, want it to contain %q", result.GetError(), want)
		}
	}
}

func TestBootstrapDropsInDeleteOrderWhenForced(t *testing.T) {
	t.Parallel()

	client, provider := contractServed(t, "1.2.3")
	bootstrapOK(t, client, &contractv1.BootstrapRequest{
		Tier:     environmentv1.Tier_TIER_PRODUCTION,
		Features: []string{fake.FeatureImages},
	})
	recordProject(t, provider, "shop", fake.FeatureImages)

	bootstrapOK(t, client, &contractv1.BootstrapRequest{
		Tier:  environmentv1.Tier_TIER_PRODUCTION,
		Force: true,
	})

	applied := provider.Bootstrap().(*fake.Bootstrapper).Applied()
	dropping := applied[len(applied)-1]
	if want := []string{fake.FeatureImages, fake.FeatureCache}; !slices.Equal(dropping.Drop, want) {
		t.Errorf("Apply() dropped %v, want %v — what stands on a feature goes first", dropping.Drop, want)
	}
	if len(dropping.Features) != 0 {
		t.Errorf("Apply() was asked for %v, want nothing left standing", dropping.Features)
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
	})
	recordProject(t, provider, "shop", fake.FeatureCache)

	described, err := client.DescribeBootstrap(ctx, &contractv1.DescribeBootstrapRequest{
		Tier:           environmentv1.Tier_TIER_PRODUCTION,
		WithDependents: true,
	})
	if err != nil {
		t.Fatalf("DescribeBootstrap() error = %v", err)
	}

	features := described.GetFeatures()
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

	status := described.GetBootstrap()
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

func TestDescribeBootstrapReportsADowngrade(t *testing.T) {
	t.Parallel()

	client, provider := contractServed(t, "1.0.0")
	bootstrapOK(t, client, &contractv1.BootstrapRequest{Tier: environmentv1.Tier_TIER_PRODUCTION})
	provider.Bootstrap().(*fake.Bootstrapper).WrittenBy("2.0.0")

	described, err := client.DescribeBootstrap(context.Background(), &contractv1.DescribeBootstrapRequest{
		Tier: environmentv1.Tier_TIER_PRODUCTION,
	})
	if err != nil {
		t.Fatalf("DescribeBootstrap() error = %v", err)
	}
	if !described.GetBootstrap().GetDowngrade() {
		t.Error("DescribeBootstrap() reports no downgrade where a newer build wrote the bootstrap")
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
	})

	plan, err := client.PlanRemoveBootstrap(ctx, &contractv1.BootstrapScope{Tier: environmentv1.Tier_TIER_PREVIEW})
	if err != nil {
		t.Fatalf("PlanRemoveBootstrap() error = %v", err)
	}
	if plan.GetSubject() != string(providerkit.ClassPreview) {
		t.Errorf("subject = %q, want the class the CLI asks the user to type back", plan.GetSubject())
	}
	if len(plan.GetItems()) != 2 {
		t.Fatalf("PlanRemoveBootstrap() planned %d items, want the feature stack and the core", len(plan.GetItems()))
	}
	for _, item := range plan.GetItems() {
		if item.GetAction() != contractv1.RemovalItem_ACTION_DELETE {
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

	described, err := client.DescribeBootstrap(ctx, &contractv1.DescribeBootstrapRequest{Tier: environmentv1.Tier_TIER_PRODUCTION})
	if err != nil {
		t.Fatalf("DescribeBootstrap() error = %v", err)
	}
	if described.GetBootstrap().GetPresent() {
		t.Error("DescribeBootstrap() still reports a bootstrap after it was removed")
	}
	if _, err := provider.Records().Read(ctx, providerkit.BootstrapRecord(providerkit.ClassProduction)); !errors.Is(err, providerkit.ErrNoRecord) {
		t.Errorf("the bootstrap record survived the removal: %v", err)
	}
}
