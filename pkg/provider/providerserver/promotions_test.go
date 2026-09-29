package providerserver_test

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	connect "connectrpc.com/connect"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/provider/ledger"
	"github.com/ocelhq/ocel/pkg/router"
)

func seedPromotions(t *testing.T, provider *fake.Provider, tier environment.Tier, slug, pointer string, ids ...string) *ledger.Ledger {
	t.Helper()
	releases := ledger.New(provider.KeyValues(), tier, slug)
	replaces := ""
	for i, id := range ids {
		if err := releases.PutStaged(context.Background(), router.DeploymentRecord{App: "web", Build: buildIdentity(i)}); err != nil {
			t.Fatal(err)
		}
		promotion := router.Promotion{PromotionID: id, Ts: int64(i + 1), Builds: map[string]string{"web": buildIdentity(i)}}
		if _, err := releases.Promote(context.Background(), promotion, pointer, replaces); err != nil {
			t.Fatal(err)
		}
		replaces = id
	}
	return releases
}

func buildIdentity(seq int) string {
	return fmt.Sprintf("%032x~%012x", seq+1, seq+1)
}

func TestListPromotionsReadsTheProjectsLedger(t *testing.T) {
	t.Parallel()
	client, provider := contractServed(t, "1.0.0")
	edgeProvisioned(t, provider, environment.TierProduction, "shop")
	seedPromotions(t, provider, environment.TierProduction, "shop", "", "p1", "p2")

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

func TestRollbackPromotesTheBuildsOfTheEarlierPromotionAsANewOne(t *testing.T) {
	t.Parallel()
	client, provider := contractServed(t, "1.0.0")
	edgeProvisioned(t, provider, environment.TierProduction, "shop")
	seedPromotions(t, provider, environment.TierProduction, "shop", "", "p1", "p2")

	rolled, err := client.Rollback(context.Background(), &contractv1.RollbackRequest{Slug: "shop"})
	if err != nil {
		t.Fatalf("Rollback() error = %v", err)
	}
	promoted := rolled.GetPromoted()
	if id := promoted.GetPromotionId(); id == "p1" || id == "p2" {
		t.Errorf("Rollback() promoted %q, want a new promotion rather than one the history already records", id)
	}
	if build := promoted.GetBuilds()["web"]; build != buildIdentity(0) {
		t.Errorf("Rollback() promoted web build %q, want %q, the build p1 promoted", build, buildIdentity(0))
	}
	if promoted.GetFlipBound() == nil {
		t.Error("Rollback() reported no flip bound, so nothing tells the user how long the flip takes")
	}

	listed, err := client.ListPromotions(context.Background(), &contractv1.ListPromotionsRequest{Slug: "shop"})
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, entry := range listed.GetPromotions() {
		ids = append(ids, entry.GetPromotion().GetPromotionId())
	}
	if want := []string{promoted.GetPromotionId(), "p2", "p1"}; !slices.Equal(ids, want) {
		t.Errorf("after the rollback the history reads %v, want %v", ids, want)
	}
	if !listed.GetPromotions()[0].GetActive() {
		t.Error("after the rollback the pointer does not name the promotion it made")
	}
}

func relayPlane(provider *fake.Provider) *fake.DataPlane {
	return provider.Routers().(*fake.Routers).DataPlane(router.Kind(fake.KindRelay))
}

func TestTheDeployFlipSpeaksThroughThePromotionStagesOwnProgress(t *testing.T) {
	builtProject(t)
	client, provider := deployServed(t)
	const marker = "the flip said this through the reporter it was handed"
	plane := relayPlane(provider)
	plane.SayOnFlip(marker)

	result, events := deploy(t, client, deployRequest())
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}
	flipped := plane.FlipProgress()
	if flipped == nil {
		t.Fatal("the deploy never reached the router's flip, so nothing was reported from it")
	}
	if flipped == progress.DiscardProgress() {
		t.Fatal("the deploy handed the flip a discarding reporter, want the Promotion stage's own")
	}

	titles := map[string]string{}
	parents := map[string]string{}
	var spoke string
	for _, scope := range startedScopes(events) {
		titles[scope.id] = scope.title
		parents[scope.id] = scope.parent
	}
	for _, event := range events {
		if saidLine(event) == marker {
			spoke = string(event.GetSpanId())
		}
	}
	if spoke == "" {
		t.Fatal("nothing the flip said reached the stream, so the flip reports through a reporter the run does not have")
	}
	promotion := "Switching traffic to promotion " + result.GetPromotionId()
	if titles[spoke] != promotion || parents[spoke] != "" {
		t.Errorf("the flip spoke on stage %q, want the unit %q", titles[spoke], promotion)
	}
}

func TestTheRollbackFlipIsHandedProgressThatDiscards(t *testing.T) {
	t.Parallel()
	client, provider := contractServed(t, "1.0.0")
	edgeProvisioned(t, provider, environment.TierProduction, "shop")
	seedPromotions(t, provider, environment.TierProduction, "shop", "", "p1", "p2")
	plane := relayPlane(provider)
	plane.SayOnFlip("the flip said this into a rollback that streams nothing")

	if _, err := client.Rollback(context.Background(), &contractv1.RollbackRequest{Slug: "shop", To: "p1"}); err != nil {
		t.Fatalf("Rollback() error = %v", err)
	}
	flipped := plane.FlipProgress()
	if flipped == nil {
		t.Fatal("the rollback never reached the router's flip")
	}
	if flipped != progress.DiscardProgress() {
		t.Errorf("the rollback handed the flip %#v, want the discarding reporter: Rollback is a unary RPC that streams nothing", flipped)
	}
}

func TestRollbackRefusesAPromotionTheHistoryDoesNotContain(t *testing.T) {
	t.Parallel()
	client, provider := contractServed(t, "1.0.0")
	edgeProvisioned(t, provider, environment.TierProduction, "shop")
	seedPromotions(t, provider, environment.TierProduction, "shop", "", "p1")

	_, err := client.Rollback(context.Background(), &contractv1.RollbackRequest{Slug: "shop", To: "p9"})
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("Rollback() = %v, want it refused as an invalid argument", err)
	}
}

func TestARollbackWhosePointerWriteLostToAChangeThatLeftItsPromotionActiveLands(t *testing.T) {
	t.Parallel()
	client, provider := contractServed(t, "1.0.0")
	edgeProvisioned(t, provider, environment.TierProduction, "shop")
	seedPromotions(t, provider, environment.TierProduction, "shop", "", "p1", "p2")

	pointer := ledger.Partition(environment.TierProduction, "shop").Key("pointers", router.DefaultPointer)
	provider.KeyValues().(*fake.KeyValues).MoveBeforeNextWrite(pointer)

	rolled, err := client.Rollback(context.Background(), &contractv1.RollbackRequest{Slug: "shop", To: "p1"})
	if err != nil {
		t.Fatalf("Rollback() against a pointer rewritten with p2 still active = %v, want it to land", err)
	}
	active, err := ledger.New(provider.KeyValues(), environment.TierProduction, "shop").ActivePromotionID(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if active != rolled.GetPromoted().GetPromotionId() {
		t.Errorf("the pointer names %q, want %q, the promotion the rollback made", active, rolled.GetPromoted().GetPromotionId())
	}
}

func TestChangingAProjectsEdgeKeepsItsPromotionHistory(t *testing.T) {
	builtProject(t)
	client, _ := deployServed(t)

	first, _ := deploy(t, client, deployRequest())
	if first == nil || !first.GetSuccess() {
		t.Fatalf("Deploy() through the relay edge = %q, want it to succeed", first.GetError())
	}
	moved := deployRequest()
	moved.Edge = &contractv1.EdgeSelection{Kind: string(fake.KindDirect)}
	second, _ := deploy(t, client, moved)
	if second == nil || !second.GetSuccess() {
		t.Fatalf("Deploy() through the direct edge = %q, want it to succeed", second.GetError())
	}

	listed, err := client.ListPromotions(context.Background(), &contractv1.ListPromotionsRequest{
		Slug: "shop",
		Edge: &contractv1.EdgeSelection{Kind: string(fake.KindDirect)},
	})
	if err != nil {
		t.Fatalf("ListPromotions() error = %v", err)
	}
	var ids []string
	for _, entry := range listed.GetPromotions() {
		ids = append(ids, entry.GetPromotion().GetPromotionId())
	}
	if !slices.Contains(ids, first.GetPromotionId()) || !slices.Contains(ids, second.GetPromotionId()) {
		t.Errorf("ListPromotions() after the edge changed = %v, want both %s (through relay) and %s (through direct): the history belongs to the project, not to the edge it deploys through",
			ids, first.GetPromotionId(), second.GetPromotionId())
	}
}

func TestRemoveStalePromotionsKeepsTheNewestN(t *testing.T) {
	t.Parallel()
	client, provider := contractServed(t, "1.0.0")
	edgeProvisioned(t, provider, environment.TierProduction, "shop")
	seedPromotions(t, provider, environment.TierProduction, "shop", "", "p1", "p2", "p3")

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

func TestAPruneSaysWhichPromotionsItReclaimedAndHowManyItKept(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		keep  int32
		seed  bool
		wants string
	}{
		{"past the newest two", 2, true, "Reclaimed promotion p1, kept 2"},
		{"when every promotion is within the newest", 5, true, "Nothing to prune: all 3 promotions are kept"},
		{"when nothing was ever deployed", 2, false, "Nothing to prune: shop has no deploy in production"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			client, provider := contractServed(t, "1.0.0")
			if tc.seed {
				edgeProvisioned(t, provider, environment.TierProduction, "shop")
				seedPromotions(t, provider, environment.TierProduction, "shop", "", "p1", "p2", "p3")
			}

			stream, err := client.RemoveStalePromotions(context.Background(), &contractv1.RemoveStalePromotionsRequest{
				Slug:        "shop",
				KeepN:       tc.keep,
				Environment: &environmentv1.Environment{Tier: environmentv1.Tier_TIER_PRODUCTION},
			})
			if err != nil {
				t.Fatalf("RemoveStalePromotions() error = %v", err)
			}
			var said []string
			for _, event := range recorded(stream) {
				if line := saidLine(event); line != "" {
					said = append(said, line)
				}
			}
			if !slices.Contains(said, tc.wants) || (tc.name == "past the newest two" && !slices.Contains(said, "Destroying the stack of web build 00000000000000000000000000000001~000000000001 (1 of 1)")) {
				t.Errorf("the prune said %q, want %q", said, tc.wants)
			}
		})
	}
}

func TestARollbackPastTheRetainedPromotionsReclaimsTheBuildItDropped(t *testing.T) {
	t.Parallel()
	client, provider := contractServed(t, "1.0.0")
	edgeProvisioned(t, provider, environment.TierProduction, "shop")
	ids := make([]string, ledger.KeptPromotions)
	for i := range ids {
		ids[i] = fmt.Sprintf("p%02d", i)
	}
	releases := seedPromotions(t, provider, environment.TierProduction, "shop", "", ids...)

	if _, err := client.Rollback(context.Background(), &contractv1.RollbackRequest{Slug: "shop"}); err != nil {
		t.Fatalf("Rollback() error = %v", err)
	}

	if _, found, err := releases.Record(context.Background(), "web", buildIdentity(0)); err != nil || found {
		t.Errorf("the record of p00's build = found %v, %v, want it removed once no promotion names it", found, err)
	}
	var destroyed []string
	for _, entry := range provider.Journal() {
		if strings.HasPrefix(entry, "destroy ") {
			destroyed = append(destroyed, entry)
		}
	}
	if len(destroyed) != 1 {
		t.Errorf("the rollback destroyed %v, want the one stack of the build it dropped", destroyed)
	}
}

func TestADeployPastTheRetainedPromotionsReclaimsTheBuildItDropped(t *testing.T) {
	builtProject(t)
	client, provider := deployServed(t)
	ids := make([]string, ledger.KeptPromotions)
	for i := range ids {
		ids[i] = fmt.Sprintf("p%02d", i)
	}
	releases := seedPromotions(t, provider, environment.TierProduction, "shop", "", ids...)

	result, events := deploy(t, client, deployRequest())
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}

	if _, found, err := releases.Record(context.Background(), "web", buildIdentity(0)); err != nil || found {
		t.Errorf("the record of p00's build = found %v, %v, want it removed once no promotion names it", found, err)
	}
	want := fmt.Sprintf("Destroying the stack of web build %s (1 of 1)", buildIdentity(0))
	var said []string
	for _, event := range events {
		if line := saidLine(event); line != "" {
			said = append(said, line)
		}
	}
	if !slices.Contains(said, want) {
		t.Errorf("the deploy said %q, want %q", said, want)
	}
}
