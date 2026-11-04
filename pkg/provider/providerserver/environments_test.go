package providerserver_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	connect "connectrpc.com/connect"

	"github.com/ocelhq/ocel/pkg/bindings"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	variablestorev1 "github.com/ocelhq/ocel/pkg/proto/provider/variablestore/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/provider/ledger"
	"github.com/ocelhq/ocel/pkg/provider/providerserver"
	"github.com/ocelhq/ocel/pkg/records"
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
		recorded, err := records.ReadOrEmpty(context.Background(), vendor.KeyValues(), name)
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
	records.Store
	rewrite func()
}

func (s *concurrentlyRewrittenStore) Write(ctx context.Context, entry records.Entry) (records.Revision, error) {
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
		if err := records.Forget(ctx, vendor.KeyValues(), key); err != nil {
			t.Fatal(err)
		}
	}}

	err := stackrecords.RecordEnvironmentMeta(ctx, store,
		environment.TierPreview, "shop", "pr-7", recordedAliasToken, "pr-123", stackrecords.LifecycleEphemeral)

	var refused refusal.Refusal
	if !errors.As(err, &refused) || !strings.Contains(refused.Message, "pr-7") {
		t.Errorf("RecordEnvironmentMeta() = %v, want a refusal naming pr-7: it was removed while this deploy recorded it", err)
	}
	if _, err := vendor.KeyValues().Read(ctx, key); !errors.Is(err, records.ErrNotFound) {
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
		pair, err := variablestoreserver.BindingPair(owner, &variablestorev1.PublishedBinding{Binding: binding})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.SetBindings(context.Background(), scope, environment, owner, []variablestore.NamedBindingWrite{{Name: binding.GetName(), Write: pair}}); err != nil {
			t.Fatal(err)
		}
	}
	inline := bindings.InlineRecordName(resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES, "orders")
	publish("pr-7", bindings.InlineRecordOwner, postgresRecord(inline, "ocel.json"))
	publish("pr-7", variablestore.OwnerOcel, postgresRecord("db--cache", ""))
	publish("pr-7", "terraform", postgresRecord("warehouse", "terraform"))
	publish("pr-8", bindings.InlineRecordOwner, postgresRecord(inline, "ocel.json"))

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

func TestRemoveEnvironmentForgetsWhatThePreviewsEnvSourceSyncCopiedThereAndKeepsWhatWasSetByHand(t *testing.T) {
	t.Parallel()
	client, vendor := contractServed(t, "1.0.0")
	deployed(t, vendor, environment.TierPreview, "shop")
	seedPromotions(t, vendor, environment.TierPreview, "shop", "pr-7", "p1")

	ctx := context.Background()
	store := variablestore.Store{KeyValues: vendor.KeyValues(), Cipher: vendor.Cipher()}
	scope := variablestore.Scope{Project: "shop", Tier: environment.TierPreview}
	copied := func(environment, key, plaintext string, second int64) {
		from := variablestore.Provenance{EnvSource: "exec", Version: plaintext, ReadAt: time.Unix(second, 0)}
		at := variablestore.Coordinate{Cell: variablestore.Cell{Key: key}, Environment: environment}
		current, err := store.Get(ctx, scope, at, false)
		if err != nil && !errors.Is(err, variablestore.ErrNotFound) {
			t.Fatal(err)
		}
		if _, err := store.SetFromEnvSource(ctx, scope, at, plaintext, from, current.Version); err != nil {
			t.Fatal(err)
		}
	}
	copied("pr-7", "TOKEN", "one", 1)
	copied("pr-7", "TOKEN", "two", 2)
	copied("pr-8", "TOKEN", "pr-8's", 1)
	copied("", "TOKEN", "tier-wide", 1)
	byHand := variablestore.Coordinate{Cell: variablestore.Cell{Key: "PAGE_ID"}, Environment: "pr-7"}
	if _, err := store.Set(ctx, scope, byHand, "set for pr-7", nil); err != nil {
		t.Fatal(err)
	}

	stream, err := client.RemoveEnvironment(ctx, &contractv1.RemoveEnvironmentRequest{
		Slug:        "shop",
		Environment: &environmentv1.Environment{Tier: environmentv1.Tier_TIER_PREVIEW, Identity: "pr-7"},
	})
	if err != nil {
		t.Fatalf("RemoveEnvironment() error = %v", err)
	}
	if result, err := drain(stream); err != nil || !result.GetSuccess() {
		t.Fatalf("RemoveEnvironment() = %v, %v", result.GetError(), err)
	}

	entries, err := vendor.KeyValues().List(ctx, variablestore.ValuesPartition(scope))
	if err != nil {
		t.Fatal(err)
	}
	var left []string
	for _, entry := range entries {
		left = append(left, strings.Join(entry.Key.Path[:4], " "))
	}
	slices.Sort(left)
	want := []string{
		"cells / PAGE_ID pr-7", "cells / TOKEN *", "cells / TOKEN pr-8",
		"history / PAGE_ID pr-7", "history / TOKEN *", "history / TOKEN pr-8",
	}
	if !slices.Equal(left, want) {
		t.Errorf("values left = %q, want %q: what pr-7's sync copied goes with it, and what was set by hand stays", left, want)
	}
}

func seedRemovablePreview(t *testing.T, vendor *fake.Provider) {
	t.Helper()
	deployed(t, vendor, environment.TierPreview, "shop")
	seedPromotions(t, vendor, environment.TierPreview, "shop", "pr-7", "p1", "p2")
	seedEnvironment(t, vendor, "shop", naming.AppStack("pr-7", "web", releaseOf(t, releaseFor(7))), naming.InfraStack("pr-7"))
	recordLabelledEnvironment(t, vendor, "pr-7", "pr-123", stackrecords.LifecyclePersistent)
}

func removePersistentPreview(t *testing.T, client contractv1connect.ProviderServiceClient) *progressv1.OperationResult {
	t.Helper()
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
	return result
}

func TestRemovingTheProductionEnvironmentIsRefusedNamingTheCommandThatRemovesProduction(t *testing.T) {
	t.Parallel()
	client, _ := contractServed(t, "1.0.0")

	stream, err := client.RemoveEnvironment(context.Background(), &contractv1.RemoveEnvironmentRequest{
		Slug:        "shop",
		Environment: &environmentv1.Environment{Tier: environmentv1.Tier_TIER_PRODUCTION},
	})
	if err != nil {
		t.Fatalf("RemoveEnvironment() error = %v", err)
	}
	result, err := drain(stream)
	said := result.GetError() + connectMessage(err)
	if result.GetSuccess() || !strings.Contains(said, "`ocel projects destroy --tier production`") {
		t.Errorf("RemoveEnvironment(production) = %q, want it refused naming the command that removes the production footprint", said)
	}
}

func TestRemoveEnvironmentIsRefusedWhileADeployHoldsIt(t *testing.T) {
	t.Parallel()
	client, vendor := contractServed(t, "1.0.0")
	seedRemovablePreview(t, vendor)
	recordLease(t, vendor.KeyValues(), environment.TierPreview, "pr-7", otherEnvironmentLease)

	result := removePersistentPreview(t, client)

	if result.GetSuccess() || !strings.Contains(result.GetError(), "a deploy to pr-7 is running: remove it again once it ends") {
		t.Fatalf("RemoveEnvironment() = %q, want it refused while a deploy holds pr-7", result.GetError())
	}
	history, err := ledger.New(vendor.KeyValues(), environment.TierPreview, "shop").History(context.Background(), "pr-7")
	if err != nil {
		t.Fatal(err)
	}
	if len(history) == 0 {
		t.Error("the refused removal dropped pr-7's promotions")
	}
}

func TestRemoveEnvironmentHoldsThePreviewWhileItRemovesItAndLeavesNoLeaseBehind(t *testing.T) {
	t.Parallel()
	client, vendor := contractServed(t, "1.0.0")
	seedRemovablePreview(t, vendor)
	var during error
	relayPlane(vendor).BeforeNextPointerRemoval(func() {
		during = takeLeaseAsDeploy(vendor.KeyValues(), environment.TierPreview, "pr-7")
	})

	if result := removePersistentPreview(t, client); !result.GetSuccess() {
		t.Fatalf("RemoveEnvironment() = %q, want pr-7 removed", result.GetError())
	}

	if during == nil || !strings.Contains(during.Error(), "a removal of pr-7 is running") {
		t.Errorf("a deploy taking pr-7 while it was removed = %v, want it refused because the removal holds pr-7", during)
	}
	if isLeaseHeld(t, vendor.KeyValues(), environment.TierPreview, "pr-7") {
		t.Error("pr-7 still records a lease after its removal, want nothing left behind")
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
	if _, err := vendor.KeyValues().Read(context.Background(), name); !errors.Is(err, records.ErrNotFound) {
		t.Errorf("reading %s after the removal = %v, want it forgotten with the environment it described", name, err)
	}

	release := releaseOf(t, releaseFor(1))
	inOrder(t, vendor.Journal(),
		"destroy "+naming.AppStack("pr-7", "web", release).String(),
		"remove "+(naming.Coordinate{Project: "shop", Env: "pr-7", App: "web", Release: release}).StoragePrefix(naming.ObjectFunction),
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
