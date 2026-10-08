package providerserver_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	connect "connectrpc.com/connect"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/progress"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/provider/ledger"
	"github.com/ocelhq/ocel/pkg/provider/providerserver"
	"github.com/ocelhq/ocel/pkg/provider/resources"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	"github.com/ocelhq/ocel/pkg/variablestore"
	"github.com/ocelhq/ocel/pkg/variablestoreserver"
)

func TestAPreviewIdentityThatNamesProductionIsRefused(t *testing.T) {
	t.Parallel()

	_, err := providerserver.EnvName(&environmentv1.Environment{
		Tier:     environmentv1.Tier_TIER_PREVIEW,
		Identity: stackrecords.ProductionEnv,
	})

	if err == nil {
		t.Fatalf("providerserver.EnvName() took %q as a preview identity, and every name a provider builds from the environment "+
			"would then read as production's", stackrecords.ProductionEnv)
	}
	if code, refused := provider.RefusedCode(err); !refused || code != refusal.CodeInvalid {
		t.Errorf("providerserver.EnvName() code = %v, want %v", code, refusal.CodeInvalid)
	}
	if !strings.Contains(err.Error(), stackrecords.ProductionEnv) {
		t.Errorf("providerserver.EnvName() = %v, want the identity it refused named", err)
	}
}

func TestAPreviewIdentityBesideProductionsIsTaken(t *testing.T) {
	t.Parallel()

	name, err := providerserver.EnvName(&environmentv1.Environment{
		Tier:     environmentv1.Tier_TIER_PREVIEW,
		Identity: "prod-1",
	})
	if err != nil {
		t.Fatalf("providerserver.EnvName(prod-1) = %v", err)
	}
	if name != "prod-1" {
		t.Errorf("providerserver.EnvName(prod-1) = %q, want the identity the caller named", name)
	}
}

func seedEnvironment(t *testing.T, vendor *fake.Provider, slug string, stacks ...naming.StackName) {
	t.Helper()
	for _, stack := range stacks {
		name := stackrecords.StackKey(environment.TierPreview, slug, stack)
		recorded, err := keyvalue.ReadOrEmpty(context.Background(), vendor.KeyValues(), name)
		if err != nil {
			t.Fatal(err)
		}
		recorded.Value = []byte("{}")
		if _, err := vendor.KeyValues().Write(context.Background(), recorded); err != nil {
			t.Fatal(err)
		}
	}
}

func TestListEnvironmentsNamesTheLifecycleEachPreviewWasCreatedWith(t *testing.T) {
	t.Parallel()
	client, vendor := contractServed(t, "1.0.0")
	release := naming.NewReleaseToken("b1", "")
	seedEnvironment(t, vendor, "shop",
		naming.AppStack(stackrecords.ProductionEnv, "web", release),
		naming.AppStack("pr-7", "web", release),
		naming.AppStack("staging", "web", release),
	)
	recordEnvironment(t, vendor, "pr-7", stackrecords.LifecycleEphemeral)
	recordEnvironment(t, vendor, "staging", stackrecords.LifecyclePersistent)

	lifecycles := listLifecycles(t, client)

	if len(lifecycles) != 2 {
		t.Fatalf("ListEnvironments() = %v, want the two previews and not production", lifecycles)
	}
	if lifecycles["pr-7"] != environmentv1.Lifecycle_LIFECYCLE_EPHEMERAL {
		t.Errorf("pr-7 is %s, want ephemeral: it was created ephemeral", lifecycles["pr-7"])
	}
	if lifecycles["staging"] != environmentv1.Lifecycle_LIFECYCLE_PERSISTENT {
		t.Errorf("staging is %s, want persistent: it was created persistent, whether or not its infra stack is recorded yet", lifecycles["staging"])
	}
}

func TestListEnvironmentsNamesNoLifecycleForAPreviewThatRecordedNone(t *testing.T) {
	t.Parallel()
	client, vendor := contractServed(t, "1.0.0")
	release := naming.NewReleaseToken("b1", "")
	seedEnvironment(t, vendor, "shop",
		naming.AppStack("staging", "web", release),
		naming.InfraStack("staging"),
	)

	lifecycles := listLifecycles(t, client)

	if lifecycles["staging"] != environmentv1.Lifecycle_LIFECYCLE_UNSPECIFIED {
		t.Errorf("staging is %s, want unspecified: nothing recorded its lifecycle, and an infra stack is not a record of one", lifecycles["staging"])
	}
}

func getEnvironment(t *testing.T, client contractv1connect.ProviderServiceClient, identity string) *contractv1.PreviewEnvironment {
	t.Helper()
	got, err := client.GetEnvironment(context.Background(), &contractv1.GetEnvironmentRequest{
		Slug:        "shop",
		Environment: &environmentv1.Environment{Tier: environmentv1.Tier_TIER_PREVIEW, Identity: identity},
	})
	if err != nil {
		t.Fatalf("GetEnvironment(%s) error = %v", identity, err)
	}
	return got.GetEnvironment()
}

func TestGetEnvironmentNamesTheLifecycleThePreviewWasCreatedWith(t *testing.T) {
	t.Parallel()
	client, vendor := contractServed(t, "1.0.0")
	recordEnvironment(t, vendor, "pr-7", stackrecords.LifecycleEphemeral)
	recordEnvironment(t, vendor, "staging", stackrecords.LifecyclePersistent)

	for identity, want := range map[string]environmentv1.Lifecycle{
		"pr-7":    environmentv1.Lifecycle_LIFECYCLE_EPHEMERAL,
		"staging": environmentv1.Lifecycle_LIFECYCLE_PERSISTENT,
	} {
		got := getEnvironment(t, client, identity)
		if got.GetIdentity() != identity || got.GetLifecycle() != want {
			t.Errorf("GetEnvironment(%s) = %v, want %s: it was created %s", identity, got, want, want)
		}
	}
}

func TestGetEnvironmentReturnsNoPreviewWhoseRecordNamesNoLifecycle(t *testing.T) {
	t.Parallel()
	client, vendor := contractServed(t, "1.0.0")
	recordEnvironment(t, vendor, "staging", "")

	if got := getEnvironment(t, client, "staging"); got != nil {
		t.Errorf("GetEnvironment(staging) = %v, want no preview: a deploy fixes the lifecycle before it provisions anything, so a record without one was never deployed", got)
	}
}

func TestGetEnvironmentReturnsNoPreviewWhereNothingIsRecorded(t *testing.T) {
	t.Parallel()
	client, _ := contractServed(t, "1.0.0")

	if got := getEnvironment(t, client, "staging"); got != nil {
		t.Errorf("GetEnvironment(staging) = %v, want no preview: nothing was ever deployed there", got)
	}
}

func TestGetEnvironmentRefusesAProductionEnvironment(t *testing.T) {
	t.Parallel()
	client, vendor := contractServed(t, "1.0.0")
	recordEnvironment(t, vendor, "production", stackrecords.LifecyclePersistent)

	got, err := client.GetEnvironment(context.Background(), &contractv1.GetEnvironmentRequest{
		Slug:        "shop",
		Environment: &environmentv1.Environment{Tier: environmentv1.Tier_TIER_PRODUCTION},
	})
	if code := connect.CodeOf(err); code != connect.CodeInvalidArgument {
		t.Errorf("GetEnvironment(production) = %v, %v; want it refused as invalid: only a preview has a lifecycle to read", got, err)
	}
}

const recordedAliasToken = "aaaaaaaaaaaaaaaa"

func recordEnvironment(t *testing.T, vendor *fake.Provider, env string, lifecycle stackrecords.Lifecycle) {
	t.Helper()
	recordLabelledEnvironment(t, vendor, env, "", lifecycle)
}

func recordLabelledEnvironment(t *testing.T, vendor *fake.Provider, env, label string, lifecycle stackrecords.Lifecycle) {
	t.Helper()
	ctx := context.Background()
	if err := stackrecords.EnsureLifecycle(ctx, vendor.KeyValues(), environment.TierPreview, "shop", env, lifecycle, recordedAliasToken); err != nil {
		t.Fatal(err)
	}
	if err := stackrecords.RecordEnvironmentMeta(ctx, vendor.KeyValues(),
		environment.TierPreview, "shop", env, recordedAliasToken, label, lifecycle); err != nil {
		t.Fatal(err)
	}
}

func listLifecycles(t *testing.T, client contractv1connect.ProviderServiceClient) map[string]environmentv1.Lifecycle {
	t.Helper()
	listed, err := client.ListEnvironments(context.Background(), &contractv1.ListEnvironmentsRequest{Slug: "shop"})
	if err != nil {
		t.Fatalf("ListEnvironments() error = %v", err)
	}
	lifecycles := map[string]environmentv1.Lifecycle{}
	for _, environment := range listed.GetEnvironments() {
		lifecycles[environment.GetIdentity()] = environment.GetLifecycle()
	}
	return lifecycles
}

func TestListEnvironmentsReturnsWhatTheDeployRecordedAboutEachPreview(t *testing.T) {
	t.Parallel()
	client, vendor := contractServed(t, "1.0.0")
	release := naming.NewReleaseToken("b1", "")
	seedEnvironment(t, vendor, "shop",
		naming.AppStack("pr-7", "web", release),
		naming.AppStack("staging", "web", release),
		naming.InfraStack("staging"),
	)
	before := time.Now().Unix()
	recordLabelledEnvironment(t, vendor, "pr-7", "pr-123", stackrecords.LifecycleEphemeral)
	recordLabelledEnvironment(t, vendor, "staging", "", stackrecords.LifecyclePersistent)

	listed, err := client.ListEnvironments(context.Background(), &contractv1.ListEnvironmentsRequest{Slug: "shop"})
	if err != nil {
		t.Fatalf("ListEnvironments() error = %v", err)
	}
	environments := map[string]*contractv1.PreviewEnvironment{}
	for _, environment := range listed.GetEnvironments() {
		environments[environment.GetIdentity()] = environment
	}

	preview := environments["pr-7"]
	if preview.GetLabel() != "pr-123" {
		t.Errorf("pr-7 is labelled %q, want the pull request it was deployed for", preview.GetLabel())
	}
	if preview.GetCreatedAt() < before {
		t.Errorf("pr-7 was created at %d, want the moment the deploy recorded it", preview.GetCreatedAt())
	}
	stamped := string(environmentRecordBytes(t, vendor, "shop", "pr-7"))
	if !strings.Contains(stamped, "created_at") {
		t.Fatalf("the record a preview deploy wrote reads %s and contains nothing this test can read an absence out of", stamped)
	}
	if strings.Contains(stamped, "expires") {
		t.Errorf("the record a preview deploy wrote reads %s, and an expiry stamped there has exactly one tier of reader: `ocel preview ls` prints it, and nothing on any box or in any account ever compares it to a clock",
			stamped)
	}
}

func TestRecordingAPreviewAgainKeepsWhenItWasCreatedAndWhatItIsCalled(t *testing.T) {
	t.Parallel()
	_, vendor := contractServed(t, "1.0.0")
	ctx := context.Background()
	recordLabelledEnvironment(t, vendor, "pr-7", "pr-123", stackrecords.LifecycleEphemeral)
	first := readEnvironmentMeta(t, vendor, "shop", "pr-7")

	if err := stackrecords.RecordEnvironmentMeta(ctx, vendor.KeyValues(),
		environment.TierPreview, "shop", "pr-7", recordedAliasToken, "", stackrecords.LifecycleEphemeral); err != nil {
		t.Fatal(err)
	}
	second := readEnvironmentMeta(t, vendor, "shop", "pr-7")

	if second.CreatedAt != first.CreatedAt {
		t.Errorf("the second deploy moved the creation to %d, want it left at %d: a preview is created once",
			second.CreatedAt, first.CreatedAt)
	}
	if second.Label != "pr-123" {
		t.Errorf("the second deploy labelled the preview %q, want the label kept: this deploy names none", second.Label)
	}
}

func TestRecordingAPreviewUnderTheOtherLifecycleIsRefusedAndKeepsTheFirst(t *testing.T) {
	t.Parallel()
	_, vendor := contractServed(t, "1.0.0")
	recordEnvironment(t, vendor, "pr-7", stackrecords.LifecycleEphemeral)

	err := stackrecords.RecordEnvironmentMeta(context.Background(), vendor.KeyValues(),
		environment.TierPreview, "shop", "pr-7", recordedAliasToken, "", stackrecords.LifecyclePersistent)

	if code, refused := provider.RefusedCode(err); !refused || code != refusal.CodeInvalid {
		t.Fatalf("RecordEnvironmentMeta() = %v, want a refusal: pr-7 was created ephemeral", err)
	}
	if meta := readEnvironmentMeta(t, vendor, "shop", "pr-7"); meta.Lifecycle != stackrecords.LifecycleEphemeral {
		t.Errorf("pr-7 records %q, want the lifecycle it was created with", meta.Lifecycle)
	}
}

type concurrentlyRewrittenStore struct {
	keyvalue.Store
	rewrite func()
}

func (s *concurrentlyRewrittenStore) Write(ctx context.Context, entry keyvalue.Entry) (keyvalue.Revision, error) {
	if rewrite := s.rewrite; rewrite != nil {
		s.rewrite = nil
		rewrite()
	}
	return s.Store.Write(ctx, entry)
}

func TestRecordingAPreviewRewrittenByAConcurrentDeployReadsItAgainAndKeepsBothWrites(t *testing.T) {
	t.Parallel()
	_, vendor := contractServed(t, "1.0.0")
	recordEnvironment(t, vendor, "pr-7", stackrecords.LifecycleEphemeral)
	ctx := context.Background()
	store := &concurrentlyRewrittenStore{Store: vendor.KeyValues(), rewrite: func() {
		if err := stackrecords.RecordEnvironmentMeta(ctx, vendor.KeyValues(),
			environment.TierPreview, "shop", "pr-7", recordedAliasToken, "pr-123", stackrecords.LifecycleEphemeral); err != nil {
			t.Fatal(err)
		}
	}}

	if err := stackrecords.RecordEnvironmentMeta(ctx, store,
		environment.TierPreview, "shop", "pr-7", recordedAliasToken, "", stackrecords.LifecycleEphemeral); err != nil {
		t.Fatalf("RecordEnvironmentMeta() = %v, want it to read pr-7 again after the concurrent deploy rewrote it", err)
	}

	if meta := readEnvironmentMeta(t, vendor, "shop", "pr-7"); meta.Label != "pr-123" {
		t.Errorf("pr-7 is labelled %q, want the concurrent deploy's pr-123 kept", meta.Label)
	}
}

func TestRecordingAPreviewRemovedWhileItIsRecordedIsRefusedAndLeavesItRemoved(t *testing.T) {
	t.Parallel()
	_, vendor := contractServed(t, "1.0.0")
	recordEnvironment(t, vendor, "pr-7", stackrecords.LifecycleEphemeral)
	ctx := context.Background()
	key := stackrecords.EnvironmentKey(environment.TierPreview, "shop", "pr-7")
	store := &concurrentlyRewrittenStore{Store: vendor.KeyValues(), rewrite: func() {
		if err := keyvalue.Forget(ctx, vendor.KeyValues(), key); err != nil {
			t.Fatal(err)
		}
	}}

	err := stackrecords.RecordEnvironmentMeta(ctx, store,
		environment.TierPreview, "shop", "pr-7", recordedAliasToken, "pr-123", stackrecords.LifecycleEphemeral)

	var refused refusal.Refusal
	if !errors.As(err, &refused) || !strings.Contains(refused.Message, "pr-7") {
		t.Errorf("RecordEnvironmentMeta() = %v, want a refusal naming pr-7: it was removed while this deploy recorded it", err)
	}
	if _, err := vendor.KeyValues().Read(ctx, key); !errors.Is(err, keyvalue.ErrNotFound) {
		t.Errorf("Read() of pr-7's record = %v, want it still removed", err)
	}
}

func environmentRecordBytes(t *testing.T, vendor *fake.Provider, slug, env string) []byte {
	t.Helper()
	recorded, err := vendor.KeyValues().Read(context.Background(), stackrecords.EnvironmentKey(environment.TierPreview, slug, env))
	if err != nil {
		t.Fatal(err)
	}
	return recorded.Value
}

func readEnvironmentMeta(t *testing.T, vendor *fake.Provider, slug, env string) stackrecords.EnvironmentMeta {
	t.Helper()
	var meta stackrecords.EnvironmentMeta
	if err := json.Unmarshal(environmentRecordBytes(t, vendor, slug, env), &meta); err != nil {
		t.Fatal(err)
	}
	return meta
}

func TestRemoveEnvironmentRemovesTheRecordsOcelKeptThere(t *testing.T) {
	t.Parallel()
	client, vendor := contractServed(t, "1.0.0")
	deployed(t, vendor, environment.TierPreview, "shop")
	seedPromotions(t, vendor, environment.TierPreview, "shop", "pr-7", "p1")

	store := variablestore.Store{KeyValues: vendor.KeyValues(), Cipher: vendor.Cipher()}
	scope := variablestore.Scope{Project: "shop", Tier: environment.TierPreview}
	publish := func(environment, owner string, binding *bindingsv1.Binding) {
		pair, err := variablestoreserver.BindingPair(owner, binding)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.SetBindings(context.Background(), scope, environment, owner, []variablestore.NamedBindingWrite{{Name: binding.GetName(), Write: pair}}); err != nil {
			t.Fatal(err)
		}
	}
	inline := naming.InlineRecordName(resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES, "orders")
	publish("pr-7", naming.InlineRecordOwner, postgresRecord(inline, "ocel.json"))
	publish("pr-7", variablestore.OwnerOcel, postgresRecord("db--cache", ""))
	publish("pr-7", "terraform", postgresRecord("warehouse", "terraform"))
	publish("pr-8", naming.InlineRecordOwner, postgresRecord(inline, "ocel.json"))

	stream, err := client.RemoveEnvironment(context.Background(), &contractv1.RemoveEnvironmentRequest{
		Slug:        "shop",
		Environment: &environmentv1.Environment{Tier: environmentv1.Tier_TIER_PREVIEW, Identity: "pr-7"},
	})
	if err != nil {
		t.Fatalf("RemoveEnvironment() error = %v", err)
	}
	if result, err := drain(stream); err != nil || !result.GetSuccess() {
		t.Fatalf("RemoveEnvironment() = %v, %v", result.GetError(), err)
	}

	names := func(environment string) []string {
		published, err := store.ListBindings(context.Background(), scope, environment)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, record := range published {
			if record.Environment == environment {
				out = append(out, record.Name)
			}
		}
		slices.Sort(out)
		return out
	}
	if got := names("pr-7"); !slices.Equal(got, []string{"warehouse"}) {
		t.Errorf("pr-7 has %v, want only the record another publisher keeps: what ocel wrote for pr-7 goes with it", got)
	}
	if got := names("pr-8"); !slices.Equal(got, []string{inline}) {
		t.Errorf("pr-8 has %v, want its own record untouched", got)
	}
}

func TestRemoveEnvironmentDropsItsPointer(t *testing.T) {
	t.Parallel()
	client, vendor := contractServed(t, "1.0.0")
	deployed(t, vendor, environment.TierPreview, "shop")
	seedPromotions(t, vendor, environment.TierPreview, "shop", "pr-7", "p1", "p2")
	outlived := naming.AppStack("pr-7", "web", releaseOf(t, releaseFor(7)))
	seedEnvironment(t, vendor, "shop", outlived, naming.InfraStack("pr-7"))
	recordLabelledEnvironment(t, vendor, "pr-7", "pr-123", stackrecords.LifecyclePersistent)

	stream, err := client.RemoveEnvironment(context.Background(), &contractv1.RemoveEnvironmentRequest{
		Slug: "shop",
		Environment: &environmentv1.Environment{
			Tier:      environmentv1.Tier_TIER_PREVIEW,
			Identity:  "pr-7",
			Lifecycle: environmentv1.Lifecycle_LIFECYCLE_PERSISTENT,
		},
	})
	if err != nil {
		t.Fatalf("RemoveEnvironment() error = %v", err)
	}
	result, err := drain(stream)
	if err != nil {
		t.Fatal(err)
	}
	if !result.GetSuccess() {
		t.Fatalf("RemoveEnvironment() = %q, want the pointer dropped", result.GetError())
	}

	history, err := ledger.New(vendor.KeyValues(), environment.TierPreview, "shop").History(context.Background(), "pr-7")
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 0 {
		t.Errorf("pr-7 still has %v, want its promotions gone with the pointer", history)
	}
	name := stackrecords.EnvironmentKey(environment.TierPreview, "shop", "pr-7")
	if _, err := vendor.KeyValues().Read(context.Background(), name); !errors.Is(err, keyvalue.ErrNotFound) {
		t.Errorf("reading %s after the removal = %v, want it forgotten with the environment it described", name, err)
	}

	release := releaseOf(t, releaseFor(1))
	inOrder(t, vendor.Journal(),
		"destroy "+naming.AppStack("pr-7", "web", release).String(),
		"remove-prefix "+(naming.Coordinate{Project: "shop", Env: "pr-7", App: "web", Release: release}).StoragePrefix(),
		"destroy "+outlived.String(),
		"destroy "+naming.InfraStack("pr-7").String(),
		"forget "+name.String())
}

func TestRemovingAPreviewRefusesWhenItsLifecycleIsNotTheOneTheRemovalWasConfirmedFor(t *testing.T) {
	t.Parallel()
	client, vendor := contractServed(t, "1.0.0")
	deployed(t, vendor, environment.TierPreview, "shop")
	seedPromotions(t, vendor, environment.TierPreview, "shop", "pr-7", "p1")
	recordLabelledEnvironment(t, vendor, "pr-7", "", stackrecords.LifecyclePersistent)

	for _, confirmed := range []environmentv1.Lifecycle{environmentv1.Lifecycle_LIFECYCLE_UNSPECIFIED, environmentv1.Lifecycle_LIFECYCLE_EPHEMERAL} {
		stream, err := client.RemoveEnvironment(context.Background(), &contractv1.RemoveEnvironmentRequest{
			Slug:        "shop",
			Environment: &environmentv1.Environment{Tier: environmentv1.Tier_TIER_PREVIEW, Identity: "pr-7", Lifecycle: confirmed},
		})
		if err != nil {
			t.Fatalf("RemoveEnvironment() error = %v", err)
		}
		result, err := drain(stream)
		if err != nil {
			t.Fatal(err)
		}
		if result.GetSuccess() || !strings.Contains(result.GetError(), "is persistent") {
			t.Fatalf("removing persistent pr-7 confirmed as %s = %q, want a refusal naming what it is now: a deploy that made it persistent after the removal was confirmed would otherwise lose it unasked", confirmed, result.GetError())
		}
	}

	history, err := ledger.New(vendor.KeyValues(), environment.TierPreview, "shop").History(context.Background(), "pr-7")
	if err != nil {
		t.Fatal(err)
	}
	if len(history) == 0 {
		t.Error("pr-7 lost its promotions to a removal that was refused")
	}
}

func inOrder(t *testing.T, journal []string, want ...string) {
	t.Helper()

	at := -1
	for _, entry := range want {
		next := slices.Index(journal, entry)
		if next < 0 {
			t.Fatalf("the teardown never reached %q; it reached %v. `ocel preview rm` is four provider calls, and a step nothing asserts is a step that can be deleted", entry, journal)
		}
		if next <= at {
			t.Fatalf("the teardown reached %q at %d, out of the order %v: a release is destroyed before the artifacts it read and the environment record that names it", entry, next, want)
		}
		at = next
	}
}

func releaseOf(t *testing.T, identity string) naming.ReleaseToken {
	t.Helper()

	build, err := provider.ParseRelease(identity)
	if err != nil {
		t.Fatal(err)
	}
	return build.Token()
}

type sweeper struct {
	mu         sync.Mutex
	reconciled []string
	forgotten  []string
}

func (s *sweeper) ProvisionContainers(context.Context, provider.StackSpec, progress.Log) ([]provider.AppContainer, error) {
	return nil, nil
}

func (s *sweeper) RemoveContainers(context.Context, provider.StackRef, []provider.AppContainer, progress.Log) error {
	return nil
}

func (s *sweeper) ReconcileImages(_ context.Context, _ provider.StackRef, app, imageRef string, _ provider.ImageStore, _ progress.Log) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reconciled = append(s.reconciled, app+" "+imageRef)
	return nil
}

func (s *sweeper) ForgetReleases(_ context.Context, _ provider.StackRef, app string, _ provider.ImageStore, _ progress.Log) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.forgotten = append(s.forgotten, app)
	return nil
}

func (s *sweeper) hooks() resources.Hooks {
	return resources.Hooks{
		Containers: &resources.ContainerHooks{Provision: s.ProvisionContainers, Remove: s.RemoveContainers},
		Retention:  &resources.ImageRetentionHooks{Reconcile: s.ReconcileImages, Forget: s.ForgetReleases},
	}
}

func (s *sweeper) swept() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.reconciled)
}

func seedContainerStack(t *testing.T, p *fake.Provider, slug, pointer, app, image string) naming.StackName {
	t.Helper()

	release := releaseOf(t, releaseFor(1))
	name := naming.AppStack(pointer, app, release)
	if err := stackrecords.Write(context.Background(), p.KeyValues(), environment.TierPreview, slug, name, stackrecords.Stack{
		Kind:         provider.StackApp,
		App:          app,
		ReleaseToken: release.String(),
		Release:      releaseFor(1),
		Containers:   []provider.AppContainer{{Name: app, Physical: name.String() + "-" + app, Image: image}},
	}); err != nil {
		t.Fatal(err)
	}
	return name
}

func removeEnvironment(t *testing.T, client contractv1connect.ProviderServiceClient, slug, pointer string) *progressv1.OperationResult {
	t.Helper()

	stream, err := client.RemoveEnvironment(context.Background(), &contractv1.RemoveEnvironmentRequest{
		Slug:        slug,
		Environment: &environmentv1.Environment{Tier: environmentv1.Tier_TIER_PREVIEW, Identity: pointer},
	})
	if err != nil {
		t.Fatalf("RemoveEnvironment() error = %v", err)
	}
	result, err := drain(stream)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestRemovingAPreviewSweepsTheImagesOfAStackItsLedgerNoLongerNames(t *testing.T) {
	t.Parallel()

	swept := &sweeper{}
	vendor := fake.NewProvider(fake.Options{Region: "nowhere"}).ResourceStacks(swept.hooks())
	client := servedProvider(t, "1.0.0", vendor)
	deployed(t, vendor, environment.TierPreview, "shop")
	stack := seedContainerStack(t, vendor, "shop", "pr-7", "web", "ghcr.io/acme/web:pr-7")

	if result := removeEnvironment(t, client, "shop", "pr-7"); !result.GetSuccess() {
		t.Fatalf("RemoveEnvironment() = %q", result.GetError())
	}

	if want := []string{"web ghcr.io/acme/web:pr-7"}; !slices.Equal(swept.swept(), want) {
		t.Errorf("the teardown reconciled %v, want %v: this preview's ledger names none of its releases any more, and the sweep is otherwise a deploy's final act — a box that is never deployed to again keeps this image forever", swept.swept(), want)
	}
	if _, found, err := stackrecords.Read(context.Background(), vendor.KeyValues(),
		environment.TierPreview, "shop", stack); err != nil || found {
		t.Errorf("%s still exists after its preview came down (%v): a teardown that reads only what the ledger last named reports success over every container it left running", stack, err)
	}
}

func TestAStackRecordThatWillNotBeForgottenStillHasItsArtifactsReclaimed(t *testing.T) {
	t.Parallel()

	client, vendor := contractServed(t, "1.0.0")
	deployed(t, vendor, environment.TierPreview, "shop")
	stack := seedContainerStack(t, vendor, "shop", "pr-7", "web", "ghcr.io/acme/web:pr-7")
	store, ok := vendor.KeyValues().(*fake.KeyValues)
	if !ok {
		t.Fatalf("this test drives the key-value store's removal refusal and the provider has a %T", vendor.KeyValues())
	}
	store.SetRemovalError(stackrecords.StackKey(environment.TierPreview, "shop", stack),
		errors.New("the key-value store answered nothing"))

	if result := removeEnvironment(t, client, "shop", "pr-7"); result.GetSuccess() {
		t.Fatal("a teardown whose stack record would not be forgotten reported success")
	}

	prefix := (naming.Coordinate{Project: "shop", Env: "pr-7", App: "web", Release: releaseOf(t, releaseFor(1))}).StoragePrefix()
	journal := vendor.Journal()
	if len(journal) == 0 {
		t.Fatal("the teardown reached the provider not at all, so the reclaim below is asserted over an empty run")
	}
	if !slices.Contains(journal, "remove-prefix "+prefix) {
		t.Errorf("the teardown reached %v and never removed %s: the release behind this stack is already destroyed, so the artifacts under its prefix are bytes no later run names — the stack record that remains is what the next run retries from, not a reason to leave them",
			journal, prefix)
	}
}

func TestAPreviewTeardownTakesItsAppStacksDownBeforeTheInfraTheyDependOn(t *testing.T) {
	t.Parallel()

	client, vendor := contractServed(t, "1.0.0")
	deployed(t, vendor, environment.TierPreview, "shop")
	app := seedContainerStack(t, vendor, "shop", "pr-7", "web", "ghcr.io/acme/web:pr-7")
	infra := naming.InfraStack("pr-7")
	seedEnvironment(t, vendor, "shop", infra)

	if result := removeEnvironment(t, client, "shop", "pr-7"); !result.GetSuccess() {
		t.Fatalf("RemoveEnvironment() = %q", result.GetError())
	}

	journal := vendor.Journal()
	appFirst, underneath := slices.Index(journal, "destroy "+app.String()), slices.Index(journal, "destroy "+infra.String())
	if appFirst < 0 || underneath < 0 {
		t.Fatalf("the teardown reached %v, want it to destroy both %s and %s: neither ordering is asserted over a run that took only one of them down", journal, app, infra)
	}
	if appFirst > underneath {
		t.Errorf("the teardown destroyed %s at %d and %s at %d: an app stack reads the network, the secrets and the database its preview's infra stack owns, so taking the infra first leaves the app's own removal reaching resources that are already gone",
			infra, underneath, app, appFirst)
	}
}

func TestASecondPreviewRemovalTakesDownWhatTheFirstOneLeftInPlace(t *testing.T) {
	t.Parallel()

	client, vendor := contractServed(t, "1.0.0")
	deployed(t, vendor, environment.TierPreview, "shop")
	seedPromotions(t, vendor, environment.TierPreview, "shop", "pr-7", "p1")
	stack := seedContainerStack(t, vendor, "shop", "pr-7", "web", "ghcr.io/acme/web:pr-7")
	vendor.FakeStacks().RefuseNextDestroy(errors.New("the box answered nothing"))

	if result := removeEnvironment(t, client, "shop", "pr-7"); result.GetSuccess() {
		t.Fatal("a teardown whose first destroy refused reported success")
	}
	if result := removeEnvironment(t, client, "shop", "pr-7"); !result.GetSuccess() {
		t.Fatalf("the second RemoveEnvironment() = %q", result.GetError())
	}

	if _, found, err := stackrecords.Read(context.Background(), vendor.KeyValues(),
		environment.TierPreview, "shop", stack); err != nil || found {
		t.Errorf("%s still exists after a second teardown that reported success (%v): the first run emptied the ledger before it fell over, so a reclaim driven off the ledger's diff has nothing left to name and every container of this preview keeps running", stack, err)
	}
}

func TestRemoveEnvironmentRefusesProduction(t *testing.T) {
	t.Parallel()
	client, vendor := contractServed(t, "1.0.0")
	deployed(t, vendor, environment.TierPreview, "shop")

	stream, err := client.RemoveEnvironment(context.Background(), &contractv1.RemoveEnvironmentRequest{
		Slug:        "shop",
		Environment: &environmentv1.Environment{Tier: environmentv1.Tier_TIER_PRODUCTION},
	})
	if err != nil {
		t.Fatalf("RemoveEnvironment() error = %v", err)
	}
	if _, err := drain(stream); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("RemoveEnvironment() = %v, want production refused as an invalid argument", err)
	}
}

func TestRemovingAPreviewSaysWhichPointerAndStacksItRemovesAndHowFarAlongItIs(t *testing.T) {
	t.Parallel()
	client, vendor := contractServed(t, "1.0.0")
	deployed(t, vendor, environment.TierPreview, "shop")
	seedPromotions(t, vendor, environment.TierPreview, "shop", "pr-7", "p1", "p2")
	seedEnvironment(t, vendor, "shop", naming.AppStack("pr-7", "web", releaseOf(t, releaseFor(7))), naming.InfraStack("pr-7"))

	stream, err := client.RemoveEnvironment(context.Background(), &contractv1.RemoveEnvironmentRequest{
		Slug:        "shop",
		Environment: &environmentv1.Environment{Tier: environmentv1.Tier_TIER_PREVIEW, Identity: "pr-7"},
	})
	if err != nil {
		t.Fatalf("RemoveEnvironment() error = %v", err)
	}
	var said []string
	for _, event := range recorded(stream) {
		if line := saidLine(event); line != "" {
			said = append(said, line)
		}
	}
	for _, want := range []string{
		"Removing the routing pointer of preview pr-7",
		"Destroying stack " + naming.InfraStack("pr-7").String() + " (2 of 2)",
		"Reclaimed promotions p2 and p1",
		"Destroying the stack of web release 00000000000000000000000000000001~000000000001 (1 of 2)",
	} {
		if !slices.Contains(said, want) {
			t.Errorf("the removal said %q, want %q among it", said, want)
		}
	}
	web := "Destroying stack " + naming.AppStack("pr-7", "web", releaseOf(t, releaseFor(7))).String() + " (1 of 2)"
	if !slices.Contains(said, web) {
		t.Errorf("the removal said %q, want %q among it", said, web)
	}
}
