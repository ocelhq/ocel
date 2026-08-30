package providerkit_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	connect "connectrpc.com/connect"

	"github.com/ocelhq/ocel/pkg/naming"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/providerkit/fake"
	"github.com/ocelhq/ocel/pkg/providerkit/ledger"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func seedPromotions(t *testing.T, provider *fake.Provider, class providerkit.Class, slug, pointer string, ids ...string) *ledger.Ledger {
	t.Helper()
	held := ledger.New(provider.Records(), class, slug)
	if err := held.EnsureSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	for i, id := range ids {
		promotion := edge.Promotion{PromotionID: id, Ts: int64(i + 1), Builds: map[string]string{"web": buildIdentity(i)}}
		if err := held.Promote(context.Background(), promotion, pointer, edge.DiscardReporter()); err != nil {
			t.Fatal(err)
		}
	}
	return held
}

func buildIdentity(seq int) string {
	return fmt.Sprintf("%032x~%012x", seq+1, seq+1)
}

func seedEnvironment(t *testing.T, provider *fake.Provider, slug string, stacks ...naming.StackName) {
	t.Helper()
	for _, stack := range stacks {
		name := providerkit.StackRecord(providerkit.ClassPreview, slug, stack)
		held, err := providerkit.Held(context.Background(), provider.Records(), name)
		if err != nil {
			t.Fatal(err)
		}
		held.Bytes = []byte("{}")
		if _, err := provider.Records().Write(context.Background(), held); err != nil {
			t.Fatal(err)
		}
	}
}

func TestListPromotionsReadsTheLedgerThroughTheEdgeStack(t *testing.T) {
	t.Parallel()
	client, provider := contractServed(t, "1.0.0")
	deployed(t, provider, providerkit.ClassProduction, "shop")
	seedPromotions(t, provider, providerkit.ClassProduction, "shop", "", "p1", "p2")

	listed, err := client.ListPromotions(context.Background(), &contractv1.ListPromotionsRequest{Slug: "shop"})
	if err != nil {
		t.Fatalf("ListPromotions() error = %v", err)
	}
	if len(listed.GetPromotions()) != 2 {
		t.Fatalf("ListPromotions() = %v, want both promotions", listed.GetPromotions())
	}
	if !listed.GetPromotions()[0].GetActive() || listed.GetPromotions()[0].GetPromotion().GetPromotionId() != "p2" {
		t.Errorf("ListPromotions() heads with %+v, want p2 active", listed.GetPromotions()[0])
	}
}

func TestListPromotionsIsEmptyForAProjectThatHasNeverDeployed(t *testing.T) {
	t.Parallel()
	client, _ := contractServed(t, "1.0.0")

	listed, err := client.ListPromotions(context.Background(), &contractv1.ListPromotionsRequest{Slug: "shop"})
	if err != nil {
		t.Fatalf("ListPromotions() error = %v", err)
	}
	if len(listed.GetPromotions()) != 0 {
		t.Errorf("ListPromotions() = %v, want nothing", listed.GetPromotions())
	}
}

func TestRollbackFlipsThePointerToTheEarlierPromotion(t *testing.T) {
	t.Parallel()
	client, provider := contractServed(t, "1.0.0")
	deployed(t, provider, providerkit.ClassProduction, "shop")
	seedPromotions(t, provider, providerkit.ClassProduction, "shop", "", "p1", "p2")

	rolled, err := client.Rollback(context.Background(), &contractv1.RollbackRequest{Slug: "shop"})
	if err != nil {
		t.Fatalf("Rollback() error = %v", err)
	}
	if rolled.GetPromoted().GetPromotionId() != "p1" {
		t.Errorf("Rollback() promoted %q, want the promotion before the active one", rolled.GetPromoted().GetPromotionId())
	}
	if rolled.GetPromoted().GetFlipBound() == nil {
		t.Error("Rollback() reported no flip bound, so nothing tells the user how long the flip takes")
	}

	listed, err := client.ListPromotions(context.Background(), &contractv1.ListPromotionsRequest{Slug: "shop"})
	if err != nil {
		t.Fatal(err)
	}
	if id := listed.GetPromotions()[0].GetPromotion().GetPromotionId(); id != "p1" {
		t.Errorf("after the rollback the pointer holds %q, want p1", id)
	}
}

func TestRollbackRefusesAPromotionTheHistoryDoesNotHold(t *testing.T) {
	t.Parallel()
	client, provider := contractServed(t, "1.0.0")
	deployed(t, provider, providerkit.ClassProduction, "shop")
	seedPromotions(t, provider, providerkit.ClassProduction, "shop", "", "p1")

	_, err := client.Rollback(context.Background(), &contractv1.RollbackRequest{Slug: "shop", To: "p9"})
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("Rollback() = %v, want it refused as an invalid argument", err)
	}
}

func TestAContendedFlipLosesExactlyOnceAndTheRetryWins(t *testing.T) {
	t.Parallel()
	client, provider := contractServed(t, "1.0.0")
	deployed(t, provider, providerkit.ClassProduction, "shop")
	seedPromotions(t, provider, providerkit.ClassProduction, "shop", "", "p1", "p2")

	pointer := providerkit.RecordName{"ledger", ledger.Scope(providerkit.ClassProduction, "shop"), "pointers", edge.DefaultPointer}
	jostled := &jostle{RecordStore: provider.Records(), at: pointer}
	provider.Edges().(*fake.Edges).Edge(fake.KindRelay).UseLedger(func(state edge.StackState) fake.Ledger {
		return ledger.New(jostled, providerkit.Class(state.Class), state.Slug)
	})

	if _, err := client.Rollback(context.Background(), &contractv1.RollbackRequest{Slug: "shop", To: "p1"}); connect.CodeOf(err) != connect.CodeAborted {
		t.Fatalf("Rollback() against a pointer another promotion moved = %v, want it refused as busy", err)
	}

	rolled, err := client.Rollback(context.Background(), &contractv1.RollbackRequest{Slug: "shop", To: "p1"})
	if err != nil {
		t.Fatalf("the second Rollback() = %v, want the flip to win once the contention is gone", err)
	}
	if rolled.GetPromoted().GetPromotionId() != "p1" {
		t.Errorf("Rollback() promoted %q, want p1", rolled.GetPromoted().GetPromotionId())
	}
}

type jostle struct {
	providerkit.RecordStore
	at   providerkit.RecordName
	once sync.Once
}

func (j *jostle) Write(ctx context.Context, record providerkit.Record) (providerkit.Revision, error) {
	if record.Name.String() == j.at.String() {
		j.once.Do(func() {
			held, err := providerkit.Held(ctx, j.RecordStore, j.at)
			if err != nil {
				return
			}
			held.Bytes = append(slices.Clone(held.Bytes), ' ')
			_, _ = j.RecordStore.Write(ctx, held)
		})
	}
	return j.RecordStore.Write(ctx, record)
}

func TestAnEdgeCarryingItsOwnLedgerStillWorks(t *testing.T) {
	t.Parallel()
	client, provider := contractServed(t, "1.0.0")
	deployed(t, provider, providerkit.ClassProduction, "shop")

	own := &memoryLedger{}
	provider.Edges().(*fake.Edges).Edge(fake.KindRelay).UseLedger(func(edge.StackState) fake.Ledger { return own })

	listed, err := client.ListPromotions(context.Background(), &contractv1.ListPromotionsRequest{Slug: "shop"})
	if err != nil {
		t.Fatalf("ListPromotions() error = %v", err)
	}
	if len(listed.GetPromotions()) != 1 || listed.GetPromotions()[0].GetPromotion().GetPromotionId() != "own-1" {
		t.Fatalf("ListPromotions() = %v, want the edge's own ledger answering", listed.GetPromotions())
	}

	pruned, err := client.RemoveStalePromotions(context.Background(), &contractv1.RemoveStalePromotionsRequest{
		Slug:        "shop",
		KeepN:       1,
		Environment: &environmentv1.Environment{Tier: environmentv1.Tier_TIER_PRODUCTION},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result, err := drain(pruned); err != nil || !result.GetSuccess() {
		t.Fatalf("RemoveStalePromotions() = %q, %v, want the edge's own ledger swept", result.GetError(), err)
	}
	if own.keptN != 1 {
		t.Errorf("the edge's own ledger was asked to keep %d, want 1", own.keptN)
	}
}

type memoryLedger struct{ keptN int }

func (*memoryLedger) SchemaVersion(context.Context) (int, error) { return edge.StoreSchemaVersion, nil }

func (*memoryLedger) PutStaged(context.Context, edge.DeploymentRecord) error { return nil }

func (*memoryLedger) History(context.Context, string) ([]edge.HistoryEntry, error) {
	return []edge.HistoryEntry{{Promotion: edge.Promotion{PromotionID: "own-1"}, Active: true}}, nil
}

func (m *memoryLedger) Prune(_ context.Context, keepN int, _ string) (edge.PruneResult, error) {
	m.keptN = keepN
	return edge.PruneResult{KeptPromotionIDs: []string{"own-1"}}, nil
}

func (*memoryLedger) Promote(context.Context, edge.Promotion, string, edge.Reporter) error {
	return nil
}

func (*memoryLedger) RemovePointer(context.Context, string) (edge.PruneResult, error) {
	return edge.PruneResult{}, nil
}

func (*memoryLedger) Destroy(context.Context) error { return nil }

func TestRemoveStalePromotionsKeepsTheNewestN(t *testing.T) {
	t.Parallel()
	client, provider := contractServed(t, "1.0.0")
	deployed(t, provider, providerkit.ClassProduction, "shop")
	seedPromotions(t, provider, providerkit.ClassProduction, "shop", "", "p1", "p2", "p3")

	stream, err := client.RemoveStalePromotions(context.Background(), &contractv1.RemoveStalePromotionsRequest{
		Slug:        "shop",
		KeepN:       2,
		Environment: &environmentv1.Environment{Tier: environmentv1.Tier_TIER_PRODUCTION},
	})
	if err != nil {
		t.Fatalf("RemoveStalePromotions() error = %v", err)
	}
	if result, err := drain(stream); err != nil || !result.GetSuccess() {
		t.Fatalf("RemoveStalePromotions() = %q, %v", result.GetError(), err)
	}

	listed, err := client.ListPromotions(context.Background(), &contractv1.ListPromotionsRequest{Slug: "shop"})
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.GetPromotions()) != 2 {
		t.Errorf("after the sweep the history holds %d promotion(s), want the 2 kept", len(listed.GetPromotions()))
	}
}

func TestListEnvironmentsNamesEveryPreviewAndItsLifecycle(t *testing.T) {
	t.Parallel()
	client, provider := contractServed(t, "1.0.0")
	release := naming.NewRelease("b1", "")
	seedEnvironment(t, provider, "shop",
		naming.AppStack(providerkit.ProductionEnv, "web", release),
		naming.AppStack("pr-7", "web", release),
		naming.AppStack("staging", "web", release),
		naming.InfraStack("staging"),
	)

	listed, err := client.ListEnvironments(context.Background(), &contractv1.ListEnvironmentsRequest{Slug: "shop"})
	if err != nil {
		t.Fatalf("ListEnvironments() error = %v", err)
	}
	if len(listed.GetEnvironments()) != 2 {
		t.Fatalf("ListEnvironments() = %v, want the two previews and not production", listed.GetEnvironments())
	}
	lifecycles := map[string]environmentv1.Lifecycle{}
	for _, environment := range listed.GetEnvironments() {
		lifecycles[environment.GetIdentity()] = environment.GetLifecycle()
	}
	if lifecycles["pr-7"] != environmentv1.Lifecycle_LIFECYCLE_EPHEMERAL {
		t.Errorf("pr-7 is %s, want ephemeral: it carries no infra stack", lifecycles["pr-7"])
	}
	if lifecycles["staging"] != environmentv1.Lifecycle_LIFECYCLE_PERSISTENT {
		t.Errorf("staging is %s, want persistent: it carries an infra stack", lifecycles["staging"])
	}
}

func TestListEnvironmentsCarriesWhatTheDeployRecordedAboutEachPreview(t *testing.T) {
	t.Parallel()
	client, provider := contractServed(t, "1.0.0")
	release := naming.NewRelease("b1", "")
	seedEnvironment(t, provider, "shop",
		naming.AppStack("pr-7", "web", release),
		naming.AppStack("staging", "web", release),
		naming.InfraStack("staging"),
	)
	before := time.Now().Unix()
	if err := providerkit.RecordEnvironmentMeta(context.Background(), provider.Records(),
		providerkit.ClassPreview, "shop", "pr-7", "pr-123", true); err != nil {
		t.Fatal(err)
	}
	if err := providerkit.RecordEnvironmentMeta(context.Background(), provider.Records(),
		providerkit.ClassPreview, "shop", "staging", "", false); err != nil {
		t.Fatal(err)
	}

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
	if want := preview.GetCreatedAt() + int64(providerkit.PreviewTTL.Seconds()); preview.GetExpiresAt() != want {
		t.Errorf("pr-7 expires at %d, want %d: an ephemeral preview lives a preview's lifetime from its deploy",
			preview.GetExpiresAt(), want)
	}
	if persistent := environments["staging"]; persistent.GetExpiresAt() != 0 {
		t.Errorf("staging expires at %d, want never: a persistent preview stands until it is removed", persistent.GetExpiresAt())
	}
}

func TestRecordingAPreviewAgainKeepsWhenItWasCreatedAndWhatItIsCalled(t *testing.T) {
	t.Parallel()
	_, provider := contractServed(t, "1.0.0")
	ctx := context.Background()
	if err := providerkit.RecordEnvironmentMeta(ctx, provider.Records(),
		providerkit.ClassPreview, "shop", "pr-7", "pr-123", true); err != nil {
		t.Fatal(err)
	}
	first := readEnvironmentMeta(t, provider, "shop", "pr-7")

	if err := providerkit.RecordEnvironmentMeta(ctx, provider.Records(),
		providerkit.ClassPreview, "shop", "pr-7", "", true); err != nil {
		t.Fatal(err)
	}
	second := readEnvironmentMeta(t, provider, "shop", "pr-7")

	if second.CreatedAt != first.CreatedAt {
		t.Errorf("the second deploy moved the creation to %d, want it left at %d: a preview is created once",
			second.CreatedAt, first.CreatedAt)
	}
	if second.ExpiresAt < first.ExpiresAt {
		t.Errorf("the second deploy expires at %d, want no earlier than %d: deploying to a preview extends it",
			second.ExpiresAt, first.ExpiresAt)
	}
	if second.Label != "pr-123" {
		t.Errorf("the second deploy labelled the preview %q, want the label kept: this deploy names none", second.Label)
	}
}

func readEnvironmentMeta(t *testing.T, provider *fake.Provider, slug, env string) providerkit.EnvironmentMeta {
	t.Helper()
	held, err := provider.Records().Read(context.Background(), providerkit.EnvironmentRecord(providerkit.ClassPreview, slug, env))
	if err != nil {
		t.Fatal(err)
	}
	var meta providerkit.EnvironmentMeta
	if err := json.Unmarshal(held.Bytes, &meta); err != nil {
		t.Fatal(err)
	}
	return meta
}

func TestRemoveEnvironmentDropsItsPointer(t *testing.T) {
	t.Parallel()
	client, provider := contractServed(t, "1.0.0")
	deployed(t, provider, providerkit.ClassPreview, "shop")
	seedPromotions(t, provider, providerkit.ClassPreview, "shop", "pr-7", "p1", "p2")
	if err := providerkit.RecordEnvironmentMeta(context.Background(), provider.Records(),
		providerkit.ClassPreview, "shop", "pr-7", "pr-123", true); err != nil {
		t.Fatal(err)
	}

	stream, err := client.RemoveEnvironment(context.Background(), &contractv1.RemoveEnvironmentRequest{
		Slug: "shop",
		Environment: &environmentv1.Environment{
			Tier:     environmentv1.Tier_TIER_PREVIEW,
			Identity: "pr-7",
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

	history, err := ledger.New(provider.Records(), providerkit.ClassPreview, "shop").History(context.Background(), "pr-7")
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 0 {
		t.Errorf("pr-7 still holds %v, want its promotions gone with the pointer", history)
	}
	name := providerkit.EnvironmentRecord(providerkit.ClassPreview, "shop", "pr-7")
	if _, err := provider.Records().Read(context.Background(), name); !errors.Is(err, providerkit.ErrNoRecord) {
		t.Errorf("reading %s after the removal = %v, want it forgotten with the environment it described", name, err)
	}
}

func TestRemoveEnvironmentRefusesProduction(t *testing.T) {
	t.Parallel()
	client, provider := contractServed(t, "1.0.0")
	deployed(t, provider, providerkit.ClassPreview, "shop")

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
