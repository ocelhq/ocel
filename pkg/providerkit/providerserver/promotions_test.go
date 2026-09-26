package providerserver_test

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	connect "connectrpc.com/connect"

	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/providerkit/fake"
	"github.com/ocelhq/ocel/pkg/providerkit/ledger"
	"github.com/ocelhq/ocel/pkg/providerkit/records"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func seedPromotions(t *testing.T, provider *fake.Provider, class edge.Class, slug, pointer string, ids ...string) *ledger.Ledger {
	t.Helper()
	releases := ledger.New(provider.Records(), class, slug)
	if err := releases.EnsureSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	for i, id := range ids {
		promotion := edge.Promotion{PromotionID: id, Ts: int64(i + 1), Builds: map[string]string{"web": buildIdentity(i)}}
		if err := releases.Promote(context.Background(), promotion, pointer, edge.DiscardProgress()); err != nil {
			t.Fatal(err)
		}
	}
	return releases
}

func buildIdentity(seq int) string {
	return fmt.Sprintf("%032x~%012x", seq+1, seq+1)
}

func TestListPromotionsReadsTheLedgerThroughTheEdgeStack(t *testing.T) {
	t.Parallel()
	client, provider := contractServed(t, "1.0.0")
	deployed(t, provider, edge.ClassProduction, "shop")
	seedPromotions(t, provider, edge.ClassProduction, "shop", "", "p1", "p2")

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
	deployed(t, provider, edge.ClassProduction, "shop")
	seedPromotions(t, provider, edge.ClassProduction, "shop", "", "p1", "p2")

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
		t.Errorf("after the rollback the pointer names %q, want p1", id)
	}
}

type capturingLedger struct {
	fake.Ledger
	marker string

	mu       sync.Mutex
	progress edge.Progress
	heard    bool
}

func (c *capturingLedger) Promote(ctx context.Context, promotion edge.Promotion, pointer string, progress edge.Progress) error {
	c.mu.Lock()
	c.progress, c.heard = progress, true
	c.mu.Unlock()
	progress.Say(c.marker)
	progress.Detail(c.marker)
	progress.Span(c.marker, time.Now(), time.Now(), nil)
	return c.Ledger.Promote(ctx, promotion, pointer, progress)
}

func (c *capturingLedger) flipped() (edge.Progress, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.progress, c.heard
}

func capturing(t *testing.T, provider *fake.Provider, class edge.Class, slug, marker string) *capturingLedger {
	t.Helper()
	capturer := &capturingLedger{Ledger: ledger.New(provider.Records(), class, slug), marker: marker}
	provider.Edges().(*fake.Edges).Edge(fake.KindRelay).UseLedger(func(edge.StackState) fake.Ledger { return capturer })
	return capturer
}

func TestTheDeployFlipSpeaksThroughThePromotionStagesOwnProgress(t *testing.T) {
	builtProject(t)
	client, provider := deployServed(t)
	const marker = "the flip said this through the reporter it was handed"
	capturer := capturing(t, provider, edge.ClassProduction, "shop", marker)

	result, events := deploy(t, client, deployRequest())
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}
	progress, heard := capturer.flipped()
	if !heard {
		t.Fatal("the deploy never reached Promote, so nothing was reported from the flip")
	}
	if progress == edge.DiscardProgress() {
		t.Fatal("the deploy handed the flip a discarding reporter, want the Promotion stage's own")
	}

	titles := map[string]string{}
	parents := map[string]string{}
	var spoke string
	for _, event := range events {
		for _, stage := range event.GetStagePlan().GetStages() {
			titles[string(stage.GetId())] = stage.GetTitle()
			parents[string(stage.GetId())] = string(stage.GetParentId())
		}
		if progress := event.GetProgress(); progress.GetMessage() == marker {
			spoke = string(progress.GetStageId())
		}
	}
	if spoke == "" {
		t.Fatal("nothing the flip said reached the stream, so the flip reports through a reporter the run does not have")
	}
	if got, want := titles[spoke], "Finalizing"; got != want {
		t.Errorf("the flip spoke on stage %q, want %q", got, want)
	}
	if got, want := titles[parents[spoke]], "Promotion"; got != want {
		t.Errorf("the flip spoke under unit %q, want %q", got, want)
	}
}

func TestTheRollbackFlipIsHandedProgressThatDiscards(t *testing.T) {
	t.Parallel()
	client, provider := contractServed(t, "1.0.0")
	deployed(t, provider, edge.ClassProduction, "shop")
	seedPromotions(t, provider, edge.ClassProduction, "shop", "", "p1", "p2")
	capturer := capturing(t, provider, edge.ClassProduction, "shop",
		"the flip said this into a rollback that streams nothing")

	if _, err := client.Rollback(context.Background(), &contractv1.RollbackRequest{Slug: "shop", To: "p1"}); err != nil {
		t.Fatalf("Rollback() error = %v", err)
	}
	progress, heard := capturer.flipped()
	if !heard {
		t.Fatal("the rollback never reached Promote")
	}
	if progress != edge.DiscardProgress() {
		t.Errorf("the rollback handed the flip %#v, want the discarding reporter: Rollback is a unary RPC that streams nothing", progress)
	}
}

func TestRollbackRefusesAPromotionTheHistoryDoesNotContain(t *testing.T) {
	t.Parallel()
	client, provider := contractServed(t, "1.0.0")
	deployed(t, provider, edge.ClassProduction, "shop")
	seedPromotions(t, provider, edge.ClassProduction, "shop", "", "p1")

	_, err := client.Rollback(context.Background(), &contractv1.RollbackRequest{Slug: "shop", To: "p9"})
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("Rollback() = %v, want it refused as an invalid argument", err)
	}
}

func TestAContendedFlipLosesExactlyOnceAndTheRetryWins(t *testing.T) {
	t.Parallel()
	client, provider := contractServed(t, "1.0.0")
	deployed(t, provider, edge.ClassProduction, "shop")
	seedPromotions(t, provider, edge.ClassProduction, "shop", "", "p1", "p2")

	pointer := records.Name{"ledger", ledger.Scope(edge.ClassProduction, "shop"), "pointers", edge.DefaultPointer}
	jostled := &jostle{Store: provider.Records(), at: pointer}
	provider.Edges().(*fake.Edges).Edge(fake.KindRelay).UseLedger(func(state edge.StackState) fake.Ledger {
		return ledger.New(jostled, state.Class, state.Slug)
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
	records.Store
	at   records.Name
	once sync.Once
}

func (j *jostle) Write(ctx context.Context, record records.Record) (records.Revision, error) {
	if record.Name.String() == j.at.String() {
		j.once.Do(func() {
			recorded, err := records.ReadOrEmpty(ctx, j.Store, j.at)
			if err != nil {
				return
			}
			recorded.Bytes = append(slices.Clone(recorded.Bytes), ' ')
			_, _ = j.Store.Write(ctx, recorded)
		})
	}
	return j.Store.Write(ctx, record)
}

func TestAnEdgeWithItsOwnLedgerStillWorks(t *testing.T) {
	t.Parallel()
	client, provider := contractServed(t, "1.0.0")
	deployed(t, provider, edge.ClassProduction, "shop")

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

func (*memoryLedger) Promote(context.Context, edge.Promotion, string, edge.Progress) error {
	return nil
}

func (*memoryLedger) RemovePointer(context.Context, string) (edge.PruneResult, error) {
	return edge.PruneResult{}, nil
}

func (*memoryLedger) Destroy(context.Context) error { return nil }

func TestRemoveStalePromotionsKeepsTheNewestN(t *testing.T) {
	t.Parallel()
	client, provider := contractServed(t, "1.0.0")
	deployed(t, provider, edge.ClassProduction, "shop")
	seedPromotions(t, provider, edge.ClassProduction, "shop", "", "p1", "p2", "p3")

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
		t.Errorf("after the sweep the history has %d promotion(s), want the 2 kept", len(listed.GetPromotions()))
	}
}
