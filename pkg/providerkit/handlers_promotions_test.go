package providerkit_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	connect "connectrpc.com/connect"

	"github.com/ocelhq/ocel/pkg/naming"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
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

type capturingLedger struct {
	fake.Ledger
	marker string

	mu     sync.Mutex
	report edge.Reporter
	heard  bool
}

func (c *capturingLedger) Promote(ctx context.Context, promotion edge.Promotion, pointer string, report edge.Reporter) error {
	c.mu.Lock()
	c.report, c.heard = report, true
	c.mu.Unlock()
	report.Say(c.marker)
	report.Detail(c.marker)
	report.Span(c.marker, time.Now(), time.Now(), nil)
	return c.Ledger.Promote(ctx, promotion, pointer, report)
}

func (c *capturingLedger) flipped() (edge.Reporter, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.report, c.heard
}

func capturing(t *testing.T, provider *fake.Provider, class providerkit.Class, slug, marker string) *capturingLedger {
	t.Helper()
	held := &capturingLedger{Ledger: ledger.New(provider.Records(), class, slug), marker: marker}
	provider.Edges().(*fake.Edges).Edge(fake.KindRelay).UseLedger(func(edge.StackState) fake.Ledger { return held })
	return held
}

func TestTheDeployFlipSpeaksThroughThePromotionStagesOwnReporter(t *testing.T) {
	builtProject(t)
	client, provider := deployServed(t)
	const marker = "the flip said this through the reporter it was handed"
	held := capturing(t, provider, providerkit.ClassProduction, "shop", marker)

	result, events := deploy(t, client, deployRequest())
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}
	report, heard := held.flipped()
	if !heard {
		t.Fatal("the deploy never reached Promote, so nothing was reported from the flip")
	}
	if report == edge.DiscardReporter() {
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
		t.Fatal("nothing the flip said reached the stream, so the flip reports through a reporter the run does not carry")
	}
	if got, want := titles[spoke], "Finalizing"; got != want {
		t.Errorf("the flip spoke on stage %q, want %q", got, want)
	}
	if got, want := titles[parents[spoke]], "Promotion"; got != want {
		t.Errorf("the flip spoke under unit %q, want %q", got, want)
	}
}

func TestTheRollbackFlipIsHandedAReporterThatDiscards(t *testing.T) {
	t.Parallel()
	client, provider := contractServed(t, "1.0.0")
	deployed(t, provider, providerkit.ClassProduction, "shop")
	seedPromotions(t, provider, providerkit.ClassProduction, "shop", "", "p1", "p2")
	held := capturing(t, provider, providerkit.ClassProduction, "shop",
		"the flip said this into a rollback that streams nothing")

	if _, err := client.Rollback(context.Background(), &contractv1.RollbackRequest{Slug: "shop", To: "p1"}); err != nil {
		t.Fatalf("Rollback() error = %v", err)
	}
	report, heard := held.flipped()
	if !heard {
		t.Fatal("the rollback never reached Promote")
	}
	if report != edge.DiscardReporter() {
		t.Errorf("the rollback handed the flip %#v, want the discarding reporter: Rollback is a unary RPC that streams nothing", report)
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
		providerkit.ClassPreview, "shop", "pr-7", "pr-123"); err != nil {
		t.Fatal(err)
	}
	if err := providerkit.RecordEnvironmentMeta(context.Background(), provider.Records(),
		providerkit.ClassPreview, "shop", "staging", ""); err != nil {
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
	stamped := string(environmentRecordBytes(t, provider, "shop", "pr-7"))
	if !strings.Contains(stamped, "created_at") {
		t.Fatalf("the record a preview deploy wrote reads %s and carries nothing this test can read an absence out of", stamped)
	}
	if strings.Contains(stamped, "expires") {
		t.Errorf("the record a preview deploy wrote reads %s, and an expiry stamped there has exactly one class of reader: `ocel preview ls` prints it, and nothing on any box or in any account ever compares it to a clock",
			stamped)
	}
}

func TestRecordingAPreviewAgainKeepsWhenItWasCreatedAndWhatItIsCalled(t *testing.T) {
	t.Parallel()
	_, provider := contractServed(t, "1.0.0")
	ctx := context.Background()
	if err := providerkit.RecordEnvironmentMeta(ctx, provider.Records(),
		providerkit.ClassPreview, "shop", "pr-7", "pr-123"); err != nil {
		t.Fatal(err)
	}
	first := readEnvironmentMeta(t, provider, "shop", "pr-7")

	if err := providerkit.RecordEnvironmentMeta(ctx, provider.Records(),
		providerkit.ClassPreview, "shop", "pr-7", ""); err != nil {
		t.Fatal(err)
	}
	second := readEnvironmentMeta(t, provider, "shop", "pr-7")

	if second.CreatedAt != first.CreatedAt {
		t.Errorf("the second deploy moved the creation to %d, want it left at %d: a preview is created once",
			second.CreatedAt, first.CreatedAt)
	}
	if second.Label != "pr-123" {
		t.Errorf("the second deploy labelled the preview %q, want the label kept: this deploy names none", second.Label)
	}
}

func environmentRecordBytes(t *testing.T, provider *fake.Provider, slug, env string) []byte {
	t.Helper()
	held, err := provider.Records().Read(context.Background(), providerkit.EnvironmentRecord(providerkit.ClassPreview, slug, env))
	if err != nil {
		t.Fatal(err)
	}
	return held.Bytes
}

func readEnvironmentMeta(t *testing.T, provider *fake.Provider, slug, env string) providerkit.EnvironmentMeta {
	t.Helper()
	var meta providerkit.EnvironmentMeta
	if err := json.Unmarshal(environmentRecordBytes(t, provider, slug, env), &meta); err != nil {
		t.Fatal(err)
	}
	return meta
}

func TestRemoveEnvironmentDropsItsPointer(t *testing.T) {
	t.Parallel()
	client, provider := contractServed(t, "1.0.0")
	deployed(t, provider, providerkit.ClassPreview, "shop")
	seedPromotions(t, provider, providerkit.ClassPreview, "shop", "pr-7", "p1", "p2")
	seedEnvironment(t, provider, "shop", naming.InfraStack("pr-7"))
	if err := providerkit.RecordEnvironmentMeta(context.Background(), provider.Records(),
		providerkit.ClassPreview, "shop", "pr-7", "pr-123"); err != nil {
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

	release := releaseOf(t, buildIdentity(1))
	inOrder(t, provider.Journal(),
		"destroy "+naming.AppStack("pr-7", "web", release).String(),
		"remove-prefix "+(naming.Coordinate{Project: "shop", Env: "pr-7", App: "web", Release: release}).StoragePrefix(),
		"destroy "+naming.InfraStack("pr-7").String(),
		"forget "+name.String())
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

func releaseOf(t *testing.T, identity string) naming.Release {
	t.Helper()

	build, err := providerkit.ParseBuild(identity)
	if err != nil {
		t.Fatal(err)
	}
	return build.Release()
}

type sweeper struct {
	mu         sync.Mutex
	reconciled []string
	forgotten  []string
}

func (s *sweeper) ProvisionContainers(context.Context, providerkit.StackPlan, providerkit.Reporter) ([]providerkit.AppContainer, error) {
	return nil, nil
}

func (s *sweeper) RemoveContainers(context.Context, providerkit.StackRef, []providerkit.AppContainer, providerkit.Reporter) error {
	return nil
}

func (s *sweeper) ReconcileImages(_ context.Context, _ providerkit.StackRef, app, coordinate string, _ providerkit.Reporter) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reconciled = append(s.reconciled, app+" "+coordinate)
	return nil
}

func (s *sweeper) ForgetReleases(_ context.Context, _ providerkit.StackRef, app string, _ providerkit.Reporter) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.forgotten = append(s.forgotten, app)
	return nil
}

func (s *sweeper) swept() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.reconciled)
}

func seedContainerStack(t *testing.T, provider *fake.Provider, slug, pointer, app, image string) naming.StackName {
	t.Helper()

	release := releaseOf(t, buildIdentity(1))
	name := naming.AppStack(pointer, app, release)
	if err := providerkit.WriteStack(context.Background(), provider.Records(), providerkit.ClassPreview, slug, name, providerkit.Stack{
		Kind:       providerkit.StackApp,
		App:        app,
		Release:    release.String(),
		Identity:   buildIdentity(1),
		Containers: []providerkit.AppContainer{{Name: app, Physical: name.String() + "-" + app, Image: image}},
	}); err != nil {
		t.Fatal(err)
	}
	return name
}

func removeEnvironment(t *testing.T, client contractv1connect.ProviderServiceClient, slug, pointer string) *progressv1.ResultEvent {
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
	provider := fake.NewProvider(fake.Options{Region: "nowhere"}).Releasing(swept)
	client := servedProvider(t, "1.0.0", provider)
	deployed(t, provider, providerkit.ClassPreview, "shop")
	stack := seedContainerStack(t, provider, "shop", "pr-7", "web", "ghcr.io/acme/web:pr-7")

	if result := removeEnvironment(t, client, "shop", "pr-7"); !result.GetSuccess() {
		t.Fatalf("RemoveEnvironment() = %q", result.GetError())
	}

	if want := []string{"web ghcr.io/acme/web:pr-7"}; !slices.Equal(swept.swept(), want) {
		t.Errorf("the teardown reconciled %v, want %v: this preview's ledger names none of its releases any more, and the sweep is otherwise a deploy's final act — a box that is never deployed to again holds this image forever", swept.swept(), want)
	}
	if _, standing, err := providerkit.ReadStack(context.Background(), provider.Records(),
		providerkit.ClassPreview, "shop", stack); err != nil || standing {
		t.Errorf("%s still stands after its preview came down (%v): a teardown that reads only what the ledger last named reports success over every container it left running", stack, err)
	}
}

func TestASecondPreviewRemovalTakesDownWhatTheFirstOneLeftStanding(t *testing.T) {
	t.Parallel()

	client, provider := contractServed(t, "1.0.0")
	deployed(t, provider, providerkit.ClassPreview, "shop")
	seedPromotions(t, provider, providerkit.ClassPreview, "shop", "pr-7", "p1")
	stack := seedContainerStack(t, provider, "shop", "pr-7", "web", "ghcr.io/acme/web:pr-7")
	provider.Releaser().RefuseDestroy(errors.New("the box answered nothing"))

	if result := removeEnvironment(t, client, "shop", "pr-7"); result.GetSuccess() {
		t.Fatal("a teardown whose first destroy refused reported success")
	}
	if result := removeEnvironment(t, client, "shop", "pr-7"); !result.GetSuccess() {
		t.Fatalf("the second RemoveEnvironment() = %q", result.GetError())
	}

	if _, standing, err := providerkit.ReadStack(context.Background(), provider.Records(),
		providerkit.ClassPreview, "shop", stack); err != nil || standing {
		t.Errorf("%s still stands after a second teardown that reported success (%v): the first run emptied the ledger before it fell over, so a reclaim driven off the ledger's diff has nothing left to name and every container of this preview keeps running", stack, err)
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
