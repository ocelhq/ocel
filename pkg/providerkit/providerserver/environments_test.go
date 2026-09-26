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

	"github.com/ocelhq/ocel/pkg/naming"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	"github.com/ocelhq/ocel/pkg/providerkit/envvars"
	"github.com/ocelhq/ocel/pkg/providerkit/envvarsserver"
	"github.com/ocelhq/ocel/pkg/providerkit/fake"
	"github.com/ocelhq/ocel/pkg/providerkit/ledger"
	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	"github.com/ocelhq/ocel/pkg/providerkit/providerserver"
	"github.com/ocelhq/ocel/pkg/providerkit/records"
	"github.com/ocelhq/ocel/pkg/providerkit/refusal"
	"github.com/ocelhq/ocel/pkg/providerkit/resources"
	"github.com/ocelhq/ocel/pkg/providerkit/stackrecords"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
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

func seedEnvironment(t *testing.T, provider *fake.Provider, slug string, stacks ...naming.StackName) {
	t.Helper()
	for _, stack := range stacks {
		name := stackrecords.StackRecord(edge.ClassPreview, slug, stack)
		held, err := records.ReadOrEmpty(context.Background(), provider.Records(), name)
		if err != nil {
			t.Fatal(err)
		}
		held.Bytes = []byte("{}")
		if _, err := provider.Records().Write(context.Background(), held); err != nil {
			t.Fatal(err)
		}
	}
}

func TestListEnvironmentsNamesEveryPreviewAndItsLifecycle(t *testing.T) {
	t.Parallel()
	client, provider := contractServed(t, "1.0.0")
	release := naming.NewRelease("b1", "")
	seedEnvironment(t, provider, "shop",
		naming.AppStack(stackrecords.ProductionEnv, "web", release),
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
	if err := stackrecords.RecordEnvironmentMeta(context.Background(), provider.Records(),
		edge.ClassPreview, "shop", "pr-7", "pr-123"); err != nil {
		t.Fatal(err)
	}
	if err := stackrecords.RecordEnvironmentMeta(context.Background(), provider.Records(),
		edge.ClassPreview, "shop", "staging", ""); err != nil {
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
	if err := stackrecords.RecordEnvironmentMeta(ctx, provider.Records(),
		edge.ClassPreview, "shop", "pr-7", "pr-123"); err != nil {
		t.Fatal(err)
	}
	first := readEnvironmentMeta(t, provider, "shop", "pr-7")

	if err := stackrecords.RecordEnvironmentMeta(ctx, provider.Records(),
		edge.ClassPreview, "shop", "pr-7", ""); err != nil {
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
	held, err := provider.Records().Read(context.Background(), stackrecords.EnvironmentRecord(edge.ClassPreview, slug, env))
	if err != nil {
		t.Fatal(err)
	}
	return held.Bytes
}

func readEnvironmentMeta(t *testing.T, provider *fake.Provider, slug, env string) stackrecords.EnvironmentMeta {
	t.Helper()
	var meta stackrecords.EnvironmentMeta
	if err := json.Unmarshal(environmentRecordBytes(t, provider, slug, env), &meta); err != nil {
		t.Fatal(err)
	}
	return meta
}

func TestRemoveEnvironmentRemovesTheRecordsOcelKeptThere(t *testing.T) {
	t.Parallel()
	client, provider := contractServed(t, "1.0.0")
	deployed(t, provider, edge.ClassPreview, "shop")
	seedPromotions(t, provider, edge.ClassPreview, "shop", "pr-7", "p1")

	store := envvars.Store{Records: provider.Records(), Cipher: provider.Cipher()}
	scope := envvars.Scope{Project: "shop", Class: edge.ClassPreview}
	publish := func(environment, owner string, binding *bindingsv1.Binding) {
		pair, err := envvarsserver.BindingPair(owner, binding)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.SetBindings(context.Background(), scope, environment, owner, []envvars.NamedBindingWrite{{Name: binding.GetName(), Write: pair}}); err != nil {
			t.Fatal(err)
		}
	}
	inline := naming.InlineRecordName(resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES, "orders")
	publish("pr-7", naming.InlineRecordOwner, postgresRecord(inline, "ocel.json"))
	publish("pr-7", envvars.OwnerOcel, postgresRecord("db--cache", ""))
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
		held, err := store.ListBindings(context.Background(), scope, environment)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, record := range held {
			if record.Environment == environment {
				out = append(out, record.Name)
			}
		}
		slices.Sort(out)
		return out
	}
	if got := names("pr-7"); !slices.Equal(got, []string{"warehouse"}) {
		t.Errorf("pr-7 holds %v, want only the record another publisher keeps: what ocel wrote for pr-7 goes with it", got)
	}
	if got := names("pr-8"); !slices.Equal(got, []string{inline}) {
		t.Errorf("pr-8 holds %v, want its own record untouched", got)
	}
}

func TestRemoveEnvironmentDropsItsPointer(t *testing.T) {
	t.Parallel()
	client, provider := contractServed(t, "1.0.0")
	deployed(t, provider, edge.ClassPreview, "shop")
	seedPromotions(t, provider, edge.ClassPreview, "shop", "pr-7", "p1", "p2")
	outlived := naming.AppStack("pr-7", "web", releaseOf(t, buildIdentity(7)))
	seedEnvironment(t, provider, "shop", outlived, naming.InfraStack("pr-7"))
	if err := stackrecords.RecordEnvironmentMeta(context.Background(), provider.Records(),
		edge.ClassPreview, "shop", "pr-7", "pr-123"); err != nil {
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

	history, err := ledger.New(provider.Records(), edge.ClassPreview, "shop").History(context.Background(), "pr-7")
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 0 {
		t.Errorf("pr-7 still holds %v, want its promotions gone with the pointer", history)
	}
	name := stackrecords.EnvironmentRecord(edge.ClassPreview, "shop", "pr-7")
	if _, err := provider.Records().Read(context.Background(), name); !errors.Is(err, records.ErrNotFound) {
		t.Errorf("reading %s after the removal = %v, want it forgotten with the environment it described", name, err)
	}

	release := releaseOf(t, buildIdentity(1))
	inOrder(t, provider.Journal(),
		"destroy "+naming.AppStack("pr-7", "web", release).String(),
		"remove-prefix "+(naming.Coordinate{Project: "shop", Env: "pr-7", App: "web", Release: release}).StoragePrefix(),
		"destroy "+outlived.String(),
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

	build, err := provider.ParseBuild(identity)
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

func (s *sweeper) ProvisionContainers(context.Context, provider.StackPlan, edge.Progress) ([]provider.AppContainer, error) {
	return nil, nil
}

func (s *sweeper) RemoveContainers(context.Context, provider.StackRef, []provider.AppContainer, edge.Progress) error {
	return nil
}

func (s *sweeper) ReconcileImages(_ context.Context, _ provider.StackRef, app, imageRef string, _ edge.Progress) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reconciled = append(s.reconciled, app+" "+imageRef)
	return nil
}

func (s *sweeper) ForgetReleases(_ context.Context, _ provider.StackRef, app string, _ edge.Progress) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.forgotten = append(s.forgotten, app)
	return nil
}

func (s *sweeper) hooks() resources.Hooks {
	return resources.Hooks{
		Containers: &resources.ContainerHooks{Provision: s.ProvisionContainers, Remove: s.RemoveContainers},
		Retention:  &resources.RetentionHooks{Reconcile: s.ReconcileImages, Forget: s.ForgetReleases},
	}
}

func (s *sweeper) swept() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.reconciled)
}

func seedContainerStack(t *testing.T, p *fake.Provider, slug, pointer, app, image string) naming.StackName {
	t.Helper()

	release := releaseOf(t, buildIdentity(1))
	name := naming.AppStack(pointer, app, release)
	if err := stackrecords.Write(context.Background(), p.Records(), edge.ClassPreview, slug, name, stackrecords.Stack{
		Kind:       provider.StackApp,
		App:        app,
		Release:    release.String(),
		Identity:   buildIdentity(1),
		Containers: []provider.AppContainer{{Name: app, Physical: name.String() + "-" + app, Image: image}},
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
	provider := fake.NewProvider(fake.Options{Region: "nowhere"}).ResourceStacks(swept.hooks())
	client := servedProvider(t, "1.0.0", provider)
	deployed(t, provider, edge.ClassPreview, "shop")
	stack := seedContainerStack(t, provider, "shop", "pr-7", "web", "ghcr.io/acme/web:pr-7")

	if result := removeEnvironment(t, client, "shop", "pr-7"); !result.GetSuccess() {
		t.Fatalf("RemoveEnvironment() = %q", result.GetError())
	}

	if want := []string{"web ghcr.io/acme/web:pr-7"}; !slices.Equal(swept.swept(), want) {
		t.Errorf("the teardown reconciled %v, want %v: this preview's ledger names none of its releases any more, and the sweep is otherwise a deploy's final act — a box that is never deployed to again holds this image forever", swept.swept(), want)
	}
	if _, standing, err := stackrecords.Read(context.Background(), provider.Records(),
		edge.ClassPreview, "shop", stack); err != nil || standing {
		t.Errorf("%s still stands after its preview came down (%v): a teardown that reads only what the ledger last named reports success over every container it left running", stack, err)
	}
}

func TestAStackRecordThatWillNotBeForgottenStillHasItsArtifactsReclaimed(t *testing.T) {
	t.Parallel()

	client, provider := contractServed(t, "1.0.0")
	deployed(t, provider, edge.ClassPreview, "shop")
	stack := seedContainerStack(t, provider, "shop", "pr-7", "web", "ghcr.io/acme/web:pr-7")
	records, held := provider.Records().(*fake.Records)
	if !held {
		t.Fatalf("this test drives the record store's removal refusal and the provider holds a %T", provider.Records())
	}
	records.RefuseRemoval(stackrecords.StackRecord(edge.ClassPreview, "shop", stack),
		errors.New("the record store answered nothing"))

	if result := removeEnvironment(t, client, "shop", "pr-7"); result.GetSuccess() {
		t.Fatal("a teardown whose stack record would not be forgotten reported success")
	}

	prefix := (naming.Coordinate{Project: "shop", Env: "pr-7", App: "web", Release: releaseOf(t, buildIdentity(1))}).StoragePrefix()
	journal := provider.Journal()
	if len(journal) == 0 {
		t.Fatal("the teardown reached the provider not at all, so the reclaim below is asserted over an empty run")
	}
	if !slices.Contains(journal, "remove-prefix "+prefix) {
		t.Errorf("the teardown reached %v and never removed %s: the release behind this stack is already destroyed, so the artifacts under its prefix are bytes no later run names — the stack record standing is what the next run retries from, not a reason to leave them",
			journal, prefix)
	}
}

func TestAPreviewTeardownTakesItsAppStacksDownBeforeTheInfraTheyStandOn(t *testing.T) {
	t.Parallel()

	client, provider := contractServed(t, "1.0.0")
	deployed(t, provider, edge.ClassPreview, "shop")
	app := seedContainerStack(t, provider, "shop", "pr-7", "web", "ghcr.io/acme/web:pr-7")
	infra := naming.InfraStack("pr-7")
	seedEnvironment(t, provider, "shop", infra)

	if result := removeEnvironment(t, client, "shop", "pr-7"); !result.GetSuccess() {
		t.Fatalf("RemoveEnvironment() = %q", result.GetError())
	}

	journal := provider.Journal()
	standing, underneath := slices.Index(journal, "destroy "+app.String()), slices.Index(journal, "destroy "+infra.String())
	if standing < 0 || underneath < 0 {
		t.Fatalf("the teardown reached %v, want it to destroy both %s and %s: neither ordering is asserted over a run that took only one of them down", journal, app, infra)
	}
	if standing > underneath {
		t.Errorf("the teardown destroyed %s at %d and %s at %d: an app stack reads the network, the secrets and the database its preview's infra stack owns, so taking the infra first leaves the app's own removal reaching resources that are already gone",
			infra, underneath, app, standing)
	}
}

func TestASecondPreviewRemovalTakesDownWhatTheFirstOneLeftStanding(t *testing.T) {
	t.Parallel()

	client, provider := contractServed(t, "1.0.0")
	deployed(t, provider, edge.ClassPreview, "shop")
	seedPromotions(t, provider, edge.ClassPreview, "shop", "pr-7", "p1")
	stack := seedContainerStack(t, provider, "shop", "pr-7", "web", "ghcr.io/acme/web:pr-7")
	provider.FakeStacks().RefuseNextDestroy(errors.New("the box answered nothing"))

	if result := removeEnvironment(t, client, "shop", "pr-7"); result.GetSuccess() {
		t.Fatal("a teardown whose first destroy refused reported success")
	}
	if result := removeEnvironment(t, client, "shop", "pr-7"); !result.GetSuccess() {
		t.Fatalf("the second RemoveEnvironment() = %q", result.GetError())
	}

	if _, standing, err := stackrecords.Read(context.Background(), provider.Records(),
		edge.ClassPreview, "shop", stack); err != nil || standing {
		t.Errorf("%s still stands after a second teardown that reported success (%v): the first run emptied the ledger before it fell over, so a reclaim driven off the ledger's diff has nothing left to name and every container of this preview keeps running", stack, err)
	}
}

func TestRemoveEnvironmentRefusesProduction(t *testing.T) {
	t.Parallel()
	client, provider := contractServed(t, "1.0.0")
	deployed(t, provider, edge.ClassPreview, "shop")

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
