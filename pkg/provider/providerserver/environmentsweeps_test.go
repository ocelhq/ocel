package providerserver_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"

	connect "connectrpc.com/connect"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/progress"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/provider/providerserver"
	"github.com/ocelhq/ocel/pkg/provider/resources"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

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

func (s *sweeper) RemoveContainers(context.Context, provider.StackRef, []provider.AppContainer, provider.ImageStore, progress.Log) error {
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

func TestRemovingAPreviewNothingIsRecordedForIsRefusedNamingItAndThePreviewsThatExist(t *testing.T) {
	t.Parallel()
	client, vendor := contractServed(t, "1.0.0")
	deployed(t, vendor, environment.TierPreview, "shop")
	seedEnvironment(t, vendor, "shop", naming.InfraStack("pr-7"), naming.InfraStack("pr-8"))
	recordLabelledEnvironment(t, vendor, "pr-8", "", stackrecords.LifecycleEphemeral)

	stream, err := client.RemoveEnvironment(context.Background(), &contractv1.RemoveEnvironmentRequest{
		Slug:        "shop",
		Environment: &environmentv1.Environment{Tier: environmentv1.Tier_TIER_PREVIEW, Identity: "pr-9"},
	})
	if err != nil {
		t.Fatalf("RemoveEnvironment() error = %v", err)
	}
	result, err := drain(stream)
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("removing pr-9, which nothing records, = %v, %v; want it refused as an invalid argument: a teardown that removed nothing must not say it tore a preview down", result, err)
	}
	for _, want := range []string{"pr-9", "nothing was torn down", "pr-7, pr-8"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("RemoveEnvironment() = %q, want it to name %q", err, want)
		}
	}
	if journal := vendor.Journal(); slices.ContainsFunc(journal, func(entry string) bool { return strings.HasPrefix(entry, "destroy ") }) {
		t.Errorf("the refused removal reached %v, want no stack destroyed", journal)
	}
}

func TestARefusedPreviewRemovalNamesThePreviewsListEnvironmentsLists(t *testing.T) {
	t.Parallel()
	client, vendor := contractServed(t, "1.0.0")
	deployed(t, vendor, environment.TierPreview, "shop")
	seedEnvironment(t, vendor, "shop", naming.InfraStack("pr-7"))
	recordLabelledEnvironment(t, vendor, "pr-10", "", stackrecords.LifecycleEphemeral)
	listed, err := client.ListEnvironments(context.Background(), &contractv1.ListEnvironmentsRequest{Slug: "shop"})
	if err != nil {
		t.Fatalf("ListEnvironments() error = %v", err)
	}
	var previews []string
	for _, each := range listed.GetEnvironments() {
		previews = append(previews, each.GetIdentity())
	}

	stream, err := client.RemoveEnvironment(context.Background(), &contractv1.RemoveEnvironmentRequest{
		Slug:        "shop",
		Environment: &environmentv1.Environment{Tier: environmentv1.Tier_TIER_PREVIEW, Identity: "pr-9"},
	})
	if err != nil {
		t.Fatalf("RemoveEnvironment() error = %v", err)
	}
	_, err = drain(stream)
	if want := "its previews are " + strings.Join(previews, ", "); err == nil || !strings.HasSuffix(err.Error(), want) {
		t.Errorf("RemoveEnvironment() = %v, want it to end %q: the previews a refusal names and the previews `ocel preview ls` shows are one list", err, want)
	}
}

func TestRemovingAPreviewWhoseRecordIsGoneStillTakesDownTheStacksAndPromotionsLeftUnderIt(t *testing.T) {
	t.Parallel()
	client, vendor := contractServed(t, "1.0.0")
	deployed(t, vendor, environment.TierPreview, "shop")
	releases := seedPromotions(t, vendor, environment.TierPreview, "shop", "pr-6", "p1")
	stack := naming.InfraStack("pr-7")
	seedEnvironment(t, vendor, "shop", stack)

	for _, pointer := range []string{"pr-6", "pr-7"} {
		if result := removeEnvironment(t, client, "shop", pointer); !result.GetSuccess() {
			t.Errorf("removing %s = %q, want what is left under it taken down", pointer, result.GetError())
		}
	}
	if history, err := releases.History(context.Background(), "pr-6"); err != nil || len(history) != 0 {
		t.Errorf("pr-6's promotions after its removal = %v, %v; want p1 reclaimed", history, err)
	}
	if !slices.Contains(vendor.Journal(), "destroy "+stack.String()) {
		t.Errorf("the teardown reached %v, want %s destroyed", vendor.Journal(), stack)
	}
}

func TestRemovingAPreviewWhoseOnlyTraceIsAStackHoldingASharedResourceDestroysThatStack(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	client, vendor := contractServed(t, "1.0.0")
	deployed(t, vendor, environment.TierPreview, "shop")
	stack := naming.AppStack("pr-7", "web", naming.NewReleaseToken("b1", ""))
	shared := records.Partition{Tier: environment.TierPreview, Root: resources.RootSharedPhysicals, Path: []string{"shop"}}
	if _, err := vendor.KeyValues().Write(ctx, records.Entry{
		Key:   shared.Key("shop-web-server"),
		Value: []byte(`{"kind":"function","name":"server","holders":{"` + stack.String() + `":"rev1"}}`),
	}); err != nil {
		t.Fatal(err)
	}

	if result := removeEnvironment(t, client, "shop", "pr-7"); !result.GetSuccess() {
		t.Fatalf("removing pr-7 = %q, want the stack still holding shop-web-server taken down", result.GetError())
	}
	if !slices.Contains(vendor.Journal(), "destroy "+stack.String()) {
		t.Errorf("the teardown reached %v, want %s destroyed", vendor.Journal(), stack)
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

func TestRemovingAPreviewWithNoPromotionSaysNothingAboutPruning(t *testing.T) {
	t.Parallel()
	client, vendor := contractServed(t, "1.0.0")
	deployed(t, vendor, environment.TierPreview, "shop")
	seedEnvironment(t, vendor, "shop", naming.InfraStack("pr-7"))

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
	if !slices.Contains(said, "Destroying stack "+naming.InfraStack("pr-7").String()+" (1 of 1)") {
		t.Fatalf("the removal said %q, want it to destroy pr-7's infra stack", said)
	}
	for _, line := range said {
		if strings.Contains(line, "prune") || strings.Contains(line, "kept") {
			t.Errorf("the removal said %q: a teardown keeps no promotion, so a line about what it kept claims something it did not do", line)
		}
	}
}

func TestRemovingAPreviewDestroysItsAppStacksConcurrentlyButNeverMoreThanTheBoundAtOnce(t *testing.T) {
	t.Parallel()
	client, vendor := contractServed(t, "1.0.0")
	deployed(t, vendor, environment.TierPreview, "shop")
	stacks := []naming.StackName{naming.InfraStack("pr-7")}
	for i := range 2 * providerserver.StackDestroyConcurrency {
		stacks = append(stacks, naming.AppStack("pr-7", fmt.Sprintf("app%d", i), releaseOf(t, releaseFor(i))))
	}
	seedEnvironment(t, vendor, "shop", stacks...)
	most := countConcurrentAppDestroys(vendor.FakeStacks())

	stream, err := client.RemoveEnvironment(context.Background(), &contractv1.RemoveEnvironmentRequest{
		Slug:        "shop",
		Environment: &environmentv1.Environment{Tier: environmentv1.Tier_TIER_PREVIEW, Identity: "pr-7"},
	})
	if err != nil {
		t.Fatalf("RemoveEnvironment() error = %v", err)
	}
	if result, err := drain(stream); err != nil || !result.GetSuccess() {
		t.Fatalf("RemoveEnvironment() = %v, %q, want the preview removed", err, result.GetError())
	}

	if most() != providerserver.StackDestroyConcurrency {
		t.Errorf("at most %d app stacks were destroyed at once, want %d", most(), providerserver.StackDestroyConcurrency)
	}
}

func TestRemoveEnvironmentKeepsTheAppsADeployRoutedBeforeItTookThePreview(t *testing.T) {
	t.Parallel()
	client, vendor := contractServed(t, "1.0.0")
	seedRemovablePreview(t, vendor)
	var routed error
	vendor.KeyValues().(*fake.KeyValues).BeforeNextWrite(stackrecords.EnvironmentLeaseKey(environment.TierPreview, "shop", "pr-7"), func() {
		routed = routeAnotherApp(vendor.KeyValues(), environment.TierPreview, "docs")
	})

	if result := removePersistentPreview(t, client); !result.GetSuccess() {
		t.Fatalf("RemoveEnvironment() = %q, want pr-7 removed", result.GetError())
	}

	if routed != nil {
		t.Fatal(routed)
	}
	if _, kept := readRoutedApps(t, vendor.KeyValues(), environment.TierPreview)["docs"]; !kept {
		t.Error("removing pr-7 dropped docs, which a deploy routed before the removal took pr-7: it wrote back what it read before it held the lease")
	}
}
