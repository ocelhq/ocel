package providerserver_test

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
	"google.golang.org/protobuf/proto"

	planv1 "github.com/ocelhq/ocel/pkg/proto/common/plan/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	"github.com/ocelhq/ocel/pkg/providerkit/arch"
	"github.com/ocelhq/ocel/pkg/providerkit/fake"
	"github.com/ocelhq/ocel/pkg/providerkit/images"
	"github.com/ocelhq/ocel/pkg/providerkit/ledger"
	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	"github.com/ocelhq/ocel/pkg/providerkit/stackrecords"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

var pushedCoordinate = "ghcr.io/acme/web:" + images.RuntimeTag("sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", []byte(fake.RuntimeBinary))

func registryDeployRequest() *contractv1.DeployRequest {
	return namingARegistry(containerDeployRequest("/"))
}

func wireContains(t *testing.T, event proto.Message, text string) bool {
	t.Helper()
	wire, err := proto.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	return bytes.Contains(wire, []byte(text))
}

func imageRows(plan *planv1.ChangePlan) []*planv1.Change {
	var rows []*planv1.Change
	for _, group := range plan.GetGroups() {
		for _, change := range group.GetChanges() {
			if change.GetKind() == provider.ImageKind {
				rows = append(rows, change)
			}
		}
	}
	return rows
}

func TestADryDeployShowsTheImagePushAsARowAndPushesNothing(t *testing.T) {
	daemonWithTheBuiltImage(t, "amd64")
	builtProject(t)
	client, p := deployServed(t)

	req := registryDeployRequest()
	req.Dry = true
	result, events := deploy(t, client, req)
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}

	rows := imageRows(lastPlan(events))
	if len(rows) != 1 || rows[0].GetName() != "web" {
		t.Fatalf("the plan shows %d image rows, want the one push this deploy makes", len(rows))
	}
	if got := rows[0].GetAction(); got != planv1.Change_ACTION_CREATE {
		t.Errorf("the image row says %q, want %q for a digest the registry does not have", got, provider.ActionCreate)
	}
	registry := p.ImageStore()
	if len(registry.Asked()) == 0 {
		t.Error("the plan drew an image row without asking the registry anything, so the row is guesswork rather than a diff")
	}
	if pushed := registry.Pushed(); len(pushed) != 0 {
		t.Errorf("a dry deploy pushed %v; preview diffs on the digest and pushes nothing", pushed)
	}
}

type muteStacks struct{}

func (muteStacks) Plan(context.Context, provider.StackSpec, edge.Progress) (provider.Plan, error) {
	return provider.Plan{}, nil
}

func (muteStacks) PlanDestroy(_ context.Context, ref provider.StackRef, _ edge.Progress) (provider.Plan, error) {
	return provider.Plan{}, nil
}

func (muteStacks) Provision(_ context.Context, spec provider.StackSpec, _ edge.Progress) (provider.StackResult, error) {
	return provider.StackResult{Containers: fake.ProvisionedContainers(spec)}, nil
}

func (muteStacks) Destroy(context.Context, provider.StackRef, edge.Progress) error {
	return nil
}

func TestTheImageRowIsTheStacksOwnAndNothingElseInventsIt(t *testing.T) {
	daemonWithTheBuiltImage(t, "amd64")
	builtProject(t)
	base := fake.NewProvider(fake.Options{Region: "nowhere"})
	client := servedBy(t, refusingStacks{Provider: base, stacks: muteStacks{}})
	bootstrappedOverRPC(t, client)

	req := registryDeployRequest()
	req.Dry = true
	result, events := deploy(t, client, req)
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}

	if rows := imageRows(lastPlan(events)); len(rows) != 0 {
		t.Fatalf("the plan shows %d image rows over stacks that declared none: a row nothing in the release program emitted cannot be verified by --dry or replayed", len(rows))
	}
}

func TestADeployPushesTheImageTheBuildProducedUnderTheRegistryCoordinate(t *testing.T) {
	daemonWithTheBuiltImage(t, "amd64")
	builtProject(t)
	client, vendor := deployServed(t)

	result, _ := deploy(t, client, registryDeployRequest())
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}

	pushed := vendor.ImageStore().Pushed()
	if len(pushed) != 1 {
		t.Fatalf("the deploy pushed %v, want the one image its container app runs", pushed)
	}
	if pushed[0].Source != containerTestImage {
		t.Errorf("the push read %q from the local store, want the image the build produced, %q", pushed[0].Source, containerTestImage)
	}
	if pushed[0].ImageRef != pushedCoordinate {
		t.Errorf("the push wrote %q, want %q", pushed[0].ImageRef, pushedCoordinate)
	}
}

func TestADigestTheRegistryAlreadyHasIsNotPushedAgain(t *testing.T) {
	daemonWithTheBuiltImage(t, "amd64")
	builtProject(t)
	client, vendor := deployServed(t)
	vendor.ImageStore().Preload(pushedCoordinate)

	result, _ := deploy(t, client, registryDeployRequest())
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}
	if pushed := vendor.ImageStore().Pushed(); len(pushed) != 0 {
		t.Errorf("the deploy pushed %v that the registry already has", pushed)
	}
}

func TestADigestTheRegistryAlreadyHasShowsOnThePlan(t *testing.T) {
	daemonWithTheBuiltImage(t, "amd64")
	builtProject(t)
	client, p := deployServed(t)
	p.ImageStore().Preload(pushedCoordinate)

	req := registryDeployRequest()
	req.Dry = true
	_, events := deploy(t, client, req)

	rows := imageRows(lastPlan(events))
	if len(rows) != 1 || rows[0].GetAction() != planv1.Change_ACTION_KEEP {
		t.Errorf("the plan shows %v for an image the registry already has, want one %q row", rows, provider.ActionKeep)
	}
}

func TestAContainerAppOnAProviderTakingNeitherPathFailsNamingTheGap(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)

	req := containerDeployRequest("/")
	req.Dry = true
	result, _, err := deployStream(t, client, req)
	if err == nil && result.GetSuccess() {
		t.Fatal("Deploy() succeeded where nothing names a registry and the provider takes no image directly, " +
			"so the plan promised a container the box would never be given an image for")
	}
	refused := result.GetError()
	if err != nil {
		refused = err.Error()
	}
	for _, want := range []string{"web", "registry"} {
		if !strings.Contains(refused, want) {
			t.Errorf("Deploy() = %q, want the gap named down to %q", refused, want)
		}
	}
	for _, want := range []string{"\u2192 name a `registry` in the project config", "`password`"} {
		if !strings.Contains(refused, want) {
			t.Errorf("Deploy() = %q, want the way out of it named down to %q: a plan-time refusal that names only the gap "+
				"leaves the reader to guess what to type", refused, want)
		}
	}
	if opened := vendor.ImageStore().Opened(); len(opened) != 0 {
		t.Errorf("a registry was opened as %v where the deploy named none", opened)
	}
}

func TestAServerlessDeployNamesNoImageToPush(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)

	req := deployRequest()
	req.ImageRegistry = &contractv1.ImageRegistry{Server: "ghcr.io", Namespace: "acme", Password: "hunter2"}
	result, _ := deploy(t, client, req)
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}
	if asked := vendor.ImageStore().Asked(); len(asked) != 0 {
		t.Errorf("a serverless deploy asked the registry about %v, and it ships zips rather than images", asked)
	}
}

func TestARegistryThatCannotBeReachedStopsTheDeploy(t *testing.T) {
	daemonWithTheBuiltImage(t, "amd64")
	builtProject(t)
	client, vendor := deployServed(t)
	vendor.ImageStore().FailPushes(errors.New("the token is not accepted"))

	result, _ := deploy(t, client, registryDeployRequest())
	if result != nil && result.GetSuccess() {
		t.Fatal("Deploy() succeeded over a registry that refused the push, so the release would promote an image nothing can pull")
	}
	if !strings.Contains(result.GetError(), "the token is not accepted") {
		t.Errorf("Deploy() = %q, want the registry's own reason", result.GetError())
	}
}

func TestThePasswordTheDeploySendsReachesTheRegistryAndNothingElse(t *testing.T) {
	daemonWithTheBuiltImage(t, "amd64")
	builtProject(t)
	client, vendor := deployServed(t)

	result, events := deploy(t, client, registryDeployRequest())
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}
	opened := vendor.ImageStore().Opened()
	if len(opened) != 1 || opened[0].Password != "hunter2" {
		t.Fatalf("the registry was opened as %v, want the one target the deploy resolved", opened)
	}
	for _, event := range events {
		if wireContains(t, event, "hunter2") {
			t.Fatal("the deploy stream contains the registry password")
		}
	}
}

func TestAnImageRowRidesInsideTheAppsOwnStackGroup(t *testing.T) {
	daemonWithTheBuiltImage(t, "amd64")
	builtProject(t)
	client, _ := deployServed(t)

	req := registryDeployRequest()
	req.Dry = true
	_, events := deploy(t, client, req)

	for _, group := range lastPlan(events).GetGroups() {
		names := changeNames(group)
		if !slices.Contains(names, provider.ImageKind+":web") {
			continue
		}
		if group.GetKind() != provider.StackGroupKind || !strings.Contains(group.GetName(), "web") {
			t.Fatalf("the image row sits in the %s group %q, want the app's own stack, so an apply of that stack is what pushes it",
				group.GetKind(), group.GetName())
		}
		return
	}
	t.Fatal("no group contains the image row")
}

func TestTheImageStoreIsOpenedFromTheTargetTheDeployNames(t *testing.T) {
	store := fake.NewImages()
	push := provider.ImagePush{App: "web", Source: containerTestImage, ImageRef: pushedCoordinate}
	plan := provider.ImagePushes{Store: store, Pushes: []provider.ImagePush{push}}

	if err := plan.PushMissing(context.Background(), nil); err != nil {
		t.Fatalf("PushMissing() = %v", err)
	}
	if err := plan.PushMissing(context.Background(), nil); err != nil {
		t.Fatalf("PushMissing() = %v", err)
	}
	if pushed := store.Pushed(); len(pushed) != 1 {
		t.Errorf("PushMissing() pushed %d times, want the second run to find the digest already there", len(pushed))
	}
}

type refusingStore struct{ where string }

func (s refusingStore) Destination() string { return s.where }

func (s refusingStore) Has(context.Context, provider.ImagePush) (bool, error) {
	return false, nil
}

func (s refusingStore) Push(context.Context, provider.ImagePush, edge.Progress) error {
	return errors.New("the stream stopped short")
}

func TestATransferThatFailsNamesWhereItWasSendingRatherThanTheCoordinate(t *testing.T) {
	store := refusingStore{where: "box.invalid"}
	plan := provider.ImagePushes{Store: store, Pushes: []provider.ImagePush{{
		App: "web", Source: containerTestImage, ImageRef: loadedCoordinate,
	}}}

	err := plan.PushMissing(context.Background(), nil)
	if err == nil {
		t.Fatal("PushMissing() = nil over a store that refuses every push")
	}
	if !strings.Contains(err.Error(), store.where) {
		t.Errorf("PushMissing() = %v, want the destination %q the deploy announced it was sending to", err, store.where)
	}
	if strings.Contains(err.Error(), loadedCoordinate) {
		t.Errorf("PushMissing() = %v: a direct transfer that failed reads as a push to a registry that was never involved", err)
	}
}

var loadedCoordinate = "ocel/shop/web:" + images.RuntimeTag("sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", []byte(fake.RuntimeBinary))

type loadingProvider struct {
	*fake.Provider
	direct *fake.Images
}

func (p loadingProvider) Hooks() provider.Hooks {
	hooks := p.Provider.Hooks()
	hooks.OpenDirectImages = p.OpenDirectImages
	return hooks
}

func (p loadingProvider) OpenDirectImages(context.Context) (provider.ImageStore, error) {
	return p.direct, nil
}

func loadServed(t *testing.T) (contractv1connect.ProviderServiceClient, loadingProvider) {
	t.Helper()
	vendor := loadingProvider{Provider: fake.NewProvider(fake.Options{Region: "nowhere"}), direct: fake.NewImages()}
	return servedBy(t, vendor), vendor
}

func TestAProviderThatTakesImagesDirectlyIsHandedTheOneTheBuildProduced(t *testing.T) {
	daemonWithTheBuiltImage(t, "amd64")
	builtProject(t)
	client, vendor := loadServed(t)

	result, _ := deploy(t, client, containerDeployRequest("/"))
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}

	handed := vendor.direct.Pushed()
	if len(handed) != 1 {
		t.Fatalf("the deploy handed over %v, want the one image its container app runs: a box with no registry account is the zero-config default", handed)
	}
	if handed[0].Source != containerTestImage {
		t.Errorf("the transfer read %q from the local store, want %q", handed[0].Source, containerTestImage)
	}
	if handed[0].ImageRef != loadedCoordinate {
		t.Errorf("the transfer landed %q, want the cli-owned coordinate %q verbatim, so release, rollback and retention never learn which path sent it",
			handed[0].ImageRef, loadedCoordinate)
	}
	if pushed := vendor.ImageStore().Pushed(); len(pushed) != 0 {
		t.Errorf("a deploy naming no registry pushed %v to one", pushed)
	}
}

func TestADigestTheBoxAlreadyHasIsNotSentAgain(t *testing.T) {
	daemonWithTheBuiltImage(t, "amd64")
	builtProject(t)
	client, vendor := loadServed(t)
	vendor.direct.Preload(loadedCoordinate)

	result, _ := deploy(t, client, containerDeployRequest("/"))
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}
	if len(vendor.direct.Asked()) == 0 {
		t.Error("the deploy streamed without asking whether the image was already there")
	}
	if handed := vendor.direct.Pushed(); len(handed) != 0 {
		t.Errorf("the redeploy sent %v again over a box that already has the digest", handed)
	}
}

func TestANamedRegistryTakesTheImageFromAProviderThatWouldOtherwiseLoadItDirectly(t *testing.T) {
	daemonWithTheBuiltImage(t, "amd64")
	builtProject(t)
	client, vendor := loadServed(t)

	result, _ := deploy(t, client, registryDeployRequest())
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}

	pushed := vendor.ImageStore().Pushed()
	if len(pushed) != 1 || pushed[0].ImageRef != pushedCoordinate {
		t.Fatalf("the deploy pushed %v, want the one registry coordinate %q", pushed, pushedCoordinate)
	}
	if handed := vendor.direct.Pushed(); len(handed) != 0 {
		t.Errorf("the same deploy also sent %v straight onto the box: the registry setting is the only switch, and both paths ran", handed)
	}
	if asked := vendor.direct.Asked(); len(asked) != 0 {
		t.Errorf("the direct store was asked about %v where a registry was named", asked)
	}
}

func TestADirectTransferShowsOnThePlanLikeAPush(t *testing.T) {
	daemonWithTheBuiltImage(t, "amd64")
	builtProject(t)
	client, p := loadServed(t)
	p.direct.Preload(loadedCoordinate)

	req := containerDeployRequest("/")
	req.Dry = true
	_, events := deploy(t, client, req)

	rows := imageRows(lastPlan(events))
	if len(rows) != 1 || rows[0].GetAction() != planv1.Change_ACTION_KEEP {
		t.Errorf("the plan shows %v for an image the box already has, want one %q row", rows, provider.ActionKeep)
	}
	if handed := p.direct.Pushed(); len(handed) != 0 {
		t.Errorf("a dry deploy sent %v onto the box", handed)
	}
}

type addressedImages struct {
	*fake.Images
	at string
}

func (a addressedImages) Destination() string { return a.at }

type addressingProvider struct {
	*fake.Provider
	direct addressedImages
}

func (p addressingProvider) Hooks() provider.Hooks {
	hooks := p.Provider.Hooks()
	hooks.OpenDirectImages = p.OpenDirectImages
	return hooks
}

func (p addressingProvider) OpenDirectImages(context.Context) (provider.ImageStore, error) {
	return p.direct, nil
}

func TestTheDeploySaysWhereTheImageWentRatherThanWhatItIsCalledThere(t *testing.T) {
	daemonWithTheBuiltImage(t, "amd64")
	builtProject(t)
	vendor := addressingProvider{
		Provider: fake.NewProvider(fake.Options{Region: "nowhere"}),
		direct:   addressedImages{Images: fake.NewImages(), at: "deploy@box.invalid"},
	}
	client := servedBy(t, vendor)

	result, events := deploy(t, client, containerDeployRequest("/"))
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}

	want := "Sending web's image to deploy@box.invalid"
	for _, event := range events {
		if event.GetProgress().GetMessage() == want {
			return
		}
	}
	t.Errorf("no event says %q: a transfer onto a machine reported as a push to %q names the coordinate where the reader expects the destination", want, loadedCoordinate)
}

type stagingLedger struct {
	fake.Ledger
	mu     sync.Mutex
	staged []edge.DeploymentRecord
}

func (l *stagingLedger) PutStaged(ctx context.Context, record edge.DeploymentRecord) error {
	l.mu.Lock()
	l.staged = append(l.staged, record)
	l.mu.Unlock()
	return l.Ledger.PutStaged(ctx, record)
}

func (l *stagingLedger) records() []edge.DeploymentRecord {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]edge.DeploymentRecord(nil), l.staged...)
}

func staging(t *testing.T, vendor *fake.Provider) *stagingLedger {
	t.Helper()
	stager := &stagingLedger{}
	vendor.Edges().(*fake.Edges).Edge(fake.KindRelay).UseLedger(func(state edge.StackState) fake.Ledger {
		stager.Ledger = ledger.New(vendor.Records(), state.Class, state.Slug)
		return stager
	})
	return stager
}

func TestAnAppSpecNamesTheCoordinateTheProviderWillStoreRatherThanTheOneTheBuildLeft(t *testing.T) {
	daemonWithTheBuiltImage(t, "amd64")
	builtProject(t)
	client, vendor := deployServed(t)

	result, _ := deploy(t, client, registryDeployRequest())
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}

	specs := vendor.FakeStacks().Provisioned()
	app := specs[len(specs)-1].App
	if app == nil {
		t.Fatal("the last spec the stacks port saw provisions no app")
	}
	if app.Image != pushedCoordinate {
		t.Errorf("Image = %q, want %q: the app is provisioned from the coordinate the push wrote, and the local digest ref names nothing the runtime can reach", app.Image, pushedCoordinate)
	}
}

func TestAnAppSpecNamesTheTaggedCoordinateADirectTransferLanded(t *testing.T) {
	daemonWithTheBuiltImage(t, "amd64")
	builtProject(t)
	client, vendor := loadServed(t)

	result, _ := deploy(t, client, containerDeployRequest("/"))
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}

	specs := vendor.FakeStacks().Provisioned()
	app := specs[len(specs)-1].App
	if app == nil {
		t.Fatal("the last spec the stacks port saw provisions no app")
	}
	if app.Image != loadedCoordinate {
		t.Errorf("Image = %q, want %q: a box takes the image under the tag the transfer landed, and the digest ref the build left names nothing the box can list, run or keep in a window", app.Image, loadedCoordinate)
	}
}

func TestTheStagedRecordNamesTheImageThatReleaseRuns(t *testing.T) {
	daemonWithTheBuiltImage(t, "amd64")
	builtProject(t)
	client, vendor := deployServed(t)
	stager := staging(t, vendor)

	result, _ := deploy(t, client, registryDeployRequest())
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}

	staged := stager.records()
	if len(staged) != 1 {
		t.Fatalf("the deploy staged %d records, want the one app it released", len(staged))
	}
	if staged[0].Image != pushedCoordinate {
		t.Errorf("the staged record names the image %q, want %q: a promotion's unit is a build identity, and a rollback that re-runs a retained digest needs the record keyed by that identity to name the ref", staged[0].Image, pushedCoordinate)
	}
}

func TestTheStagedRecordNamesTheHealthPathTheReleaseIsGatedOn(t *testing.T) {
	daemonWithTheBuiltImage(t, "amd64")
	builtProject(t)
	client, vendor := deployServed(t)
	stager := staging(t, vendor)

	result, _ := deploy(t, client, namingARegistry(containerDeployRequest("/healthz")))
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}

	staged := stager.records()
	if len(staged) != 1 {
		t.Fatalf("the deploy staged %d records, want the one app it released", len(staged))
	}
	if staged[0].HealthPath != "/healthz" {
		t.Errorf("the staged record names the health path %q, want %q: a rollback re-gates a container this deploy is not running to name the path for, and a promotion that cannot name it cannot gate at all", staged[0].HealthPath, "/healthz")
	}
}

func TestTheStagedRecordNamesTheContainerTheReleaseProvisioned(t *testing.T) {
	daemonWithTheBuiltImage(t, "amd64")
	builtProject(t)
	client, vendor := deployServed(t)
	stager := staging(t, vendor)

	result, _ := deploy(t, client, registryDeployRequest())
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}

	staged := stager.records()
	if len(staged) != 1 {
		t.Fatalf("the deploy staged %d records, want the one app it released", len(staged))
	}
	entries, err := stackrecords.List(context.Background(), vendor.Records(), edge.ClassProduction, "shop")
	if err != nil {
		t.Fatal(err)
	}
	var appContainer string
	for _, entry := range entries {
		for _, container := range entry.Containers {
			if container.Name == staged[0].App {
				appContainer = container.Physical
			}
		}
	}
	if appContainer == "" {
		t.Fatal("the deploy started no container at all, and this test needs one to name")
	}
	if staged[0].Physical != appContainer {
		t.Errorf("the staged record names the container %q, want %q: a rollback re-points the container the release started, and a name derived from anything else starts a second one beside it", staged[0].Physical, appContainer)
	}
}

func TestAServerlessRecordNamesNoImage(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)
	stager := staging(t, vendor)

	result, _ := deploy(t, client, deployRequest())
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}

	for _, record := range stager.records() {
		if record.Image != "" {
			t.Errorf("%s staged the image %q, and a serverless release runs none", record.App, record.Image)
		}
	}
}

var containerRuntimeBytes = []byte("the ocel container runtime")

type builtImageDaemon struct {
	mu       sync.Mutex
	exported int
}

func (d *builtImageDaemon) exports() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.exported
}

func savedImage(t *testing.T) []byte {
	t.Helper()
	base := baseContainer(t, v1.Config{Entrypoint: []string{"/app/server"}, Env: []string{"PATH=/usr/bin"}})
	ref, err := name.NewTag("ocel/shop/web:saved")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "image.tar")
	if err := tarball.WriteToFile(path, ref, base); err != nil {
		t.Fatal(err)
	}
	saved, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return saved
}

func daemonWithTheBuiltImage(t *testing.T, architecture string) *builtImageDaemon {
	t.Helper()
	saved := savedImage(t)
	daemon := &builtImageDaemon{}
	daemonServing(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/json"):
			_, _ = w.Write([]byte(`{"Architecture":"` + architecture + `","Os":"linux"}`))
		case strings.HasSuffix(r.URL.Path, "/get"):
			daemon.mu.Lock()
			daemon.exported++
			daemon.mu.Unlock()
			_, _ = w.Write(saved)
		default:
			http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotFound)
		}
	})
	return daemon
}

func wrappingServed(t *testing.T) (contractv1connect.ProviderServiceClient, *fake.Provider) {
	t.Helper()
	return wrappingServedOn(t, "amd64")
}

func wrappingServedOn(t *testing.T, architecture string) (contractv1connect.ProviderServiceClient, *fake.Provider) {
	t.Helper()
	vendor := fake.NewProvider(fake.Options{Region: "nowhere"})
	client := servedBy(t, vendor.WrappingContainers(architecture, containerRuntimeBytes))
	return client, vendor
}

func wrappedCoordinate() string {
	_, digest, _ := strings.Cut(containerTestImage, "@")
	return "ghcr.io/acme/web:" + images.RuntimeTag(digest, containerRuntimeBytes)
}

func TestAWrappingProviderPushesTheImageUnderTheCoordinateTheRuntimeItShipsNames(t *testing.T) {
	builtProject(t)
	daemon := daemonWithTheBuiltImage(t, "amd64")
	client, vendor := wrappingServed(t)

	result, _ := deploy(t, client, registryDeployRequest())
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}

	asked := vendor.ImageStore().Asked()
	if len(asked) != 1 {
		t.Fatalf("the deploy asked the registry about %v, want the one image its container app runs", asked)
	}
	if asked[0].ImageRef != wrappedCoordinate() {
		t.Errorf("the deploy asked about %q, want %q: a wrapped image is reached under a tag naming the runtime it boots through", asked[0].ImageRef, wrappedCoordinate())
	}
	if asked[0].Wrap == nil {
		t.Error("the push includes no wrap, so the image would reach the registry without the runtime the tag promises")
	}
	if asked[0].Digest != "" {
		t.Errorf("the push pins %q before the wrap has run, and the digest it is pushed under is only known once the image is built", asked[0].Digest)
	}
	pushed := vendor.ImageStore().Pushed()
	if len(pushed) != 1 || pushed[0].Built == nil {
		t.Fatalf("the store was handed %v, want the wrapped image itself rather than a coordinate to copy", pushed)
	}
	if daemon.exports() != 1 {
		t.Errorf("the deploy read the image out of the daemon %d times, want the one export the wrap needs", daemon.exports())
	}
}

func TestTheArchitectureTheDaemonNamesIsWhatTheRuntimeIsAskedFor(t *testing.T) {
	builtProject(t)
	daemonWithTheBuiltImage(t, "arm64")
	client, vendor := wrappingServedOn(t, "arm64")

	result, _ := deploy(t, client, registryDeployRequest())
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}

	if asked := vendor.WrappedFor(); len(asked) == 0 || asked[0] != "arm64" {
		t.Errorf("the provider was asked for a runtime built for %v, want the architecture the daemon says the image is built for: a runtime built for another one cannot execute", asked)
	}
}

func TestAnImageBuiltForAnArchitectureTheTargetDoesNotRunIsRefusedBeforeItIsPushed(t *testing.T) {
	builtProject(t)
	daemon := daemonWithTheBuiltImage(t, "arm64")
	client, vendor := wrappingServedOn(t, "amd64")

	_, _, err := deployStream(t, client, registryDeployRequest())
	if err == nil {
		t.Fatal("Deploy() succeeded, want it refused: the target cannot execute an image built for another architecture")
	}
	for _, named := range []string{"linux/arm64", "linux/amd64"} {
		if !strings.Contains(err.Error(), named) {
			t.Errorf("Deploy() refused with %q, want it to name %s so the mismatch reads at a glance", err, named)
		}
	}
	if pushed := vendor.ImageStore().Pushed(); len(pushed) != 0 {
		t.Errorf("the store was handed %v, want nothing pushed for an image the target cannot run", pushed)
	}
	if daemon.exports() != 0 {
		t.Errorf("the deploy read the image out of the daemon %d times, want none for an image it refuses", daemon.exports())
	}
}

func TestAnAppsOwnDeclaredArchitectureIsTheOneItsImageIsBuiltFor(t *testing.T) {
	builtProject(t)
	daemonWithTheBuiltImage(t, "arm64")
	client, vendor := wrappingServedOn(t, "amd64")

	req := registryDeployRequest()
	for _, container := range req.GetManifest().GetContainers() {
		container.Arch = arch.ARM64
	}
	result, _ := deploy(t, client, req)
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed: the app declares arm64 and its image is built for it", result.GetError())
	}
	if asked := vendor.WrappedFor(); len(asked) == 0 || asked[0] != "arm64" {
		t.Errorf("the provider was asked for a runtime built for %v, want arm64", asked)
	}
}

func TestAWrappedContainerRunsTheCoordinateItWasPushedUnder(t *testing.T) {
	builtProject(t)
	daemonWithTheBuiltImage(t, "amd64")
	client, vendor := wrappingServed(t)

	result, _ := deploy(t, client, registryDeployRequest())
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}

	specs := vendor.FakeStacks().Provisioned()
	var image string
	for i := len(specs) - 1; i >= 0; i-- {
		if specs[i].App != nil {
			image = specs[i].App.Image
			break
		}
	}
	if image != wrappedCoordinate() {
		t.Errorf("the app spec runs %q, want %q: the box pulls the wrapped image rather than the one the build produced", image, wrappedCoordinate())
	}
}

func TestAWrappedCoordinateTheRegistryAlreadyHasIsNeitherWrappedNorPushed(t *testing.T) {
	builtProject(t)
	daemon := daemonWithTheBuiltImage(t, "amd64")
	client, vendor := wrappingServed(t)
	vendor.ImageStore().Preload(wrappedCoordinate())

	result, _ := deploy(t, client, registryDeployRequest())
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}

	if pushed := vendor.ImageStore().Pushed(); len(pushed) != 0 {
		t.Errorf("the deploy pushed %v that the registry already has", pushed)
	}
	if daemon.exports() != 0 {
		t.Errorf("the deploy exported the image %d times for a coordinate the registry already has: wrapping reads the whole image off the daemon, and nothing is going to be pushed", daemon.exports())
	}
}

type stubStore struct {
	present bool
	pushed  []provider.ImagePush
}

func (s *stubStore) Has(context.Context, provider.ImagePush) (bool, error) {
	return s.present, nil
}

func (s *stubStore) Destination() string { return "the stub registry" }

func (s *stubStore) Push(_ context.Context, push provider.ImagePush, _ edge.Progress) error {
	s.pushed = append(s.pushed, push)
	return nil
}

func TestShipHandsTheStoreTheWrappedImageAndClearsUpAfterIt(t *testing.T) {
	t.Parallel()

	store := &stubStore{}
	cleaned := false
	plan := provider.ImagePushes{Store: store, Pushes: []provider.ImagePush{{
		App:      "web",
		ImageRef: "ghcr.io/acme/web:sha256-abc-ocel-0123456789ab",
		Wrap: func(context.Context) (v1.Image, func(), error) {
			return empty.Image, func() { cleaned = true }, nil
		},
	}}}

	if err := plan.PushMissing(context.Background(), nil); err != nil {
		t.Fatalf("PushMissing() = %v", err)
	}
	if len(store.pushed) != 1 || store.pushed[0].Built != empty.Image {
		t.Fatalf("the store was handed %v, want the image the wrap built", store.pushed)
	}
	if !cleaned {
		t.Error("the wrap's own cleanup never ran, so every wrapped push leaves the exported image behind on disk")
	}
}

func TestShipRunsNoWrapForACoordinateTheStoreAlreadyHas(t *testing.T) {
	t.Parallel()

	store := &stubStore{present: true}
	plan := provider.ImagePushes{Store: store, Pushes: []provider.ImagePush{{
		App:      "web",
		ImageRef: "ghcr.io/acme/web:sha256-abc-ocel-0123456789ab",
		Wrap: func(context.Context) (v1.Image, func(), error) {
			return nil, nil, errors.New("the image was wrapped for a push that was never needed")
		},
	}}}

	if err := plan.PushMissing(context.Background(), nil); err != nil {
		t.Fatalf("PushMissing() = %v", err)
	}
	if len(store.pushed) != 0 {
		t.Errorf("the store was handed %v for a coordinate it already has", store.pushed)
	}
}

func baseContainer(t *testing.T, config v1.Config) v1.Image {
	t.Helper()
	base, err := mutate.Config(empty.Image, config)
	if err != nil {
		t.Fatal(err)
	}
	return base
}

func daemonServing(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	daemon := httptest.NewServer(handler)
	t.Cleanup(daemon.Close)
	t.Setenv(images.DockerTLSVerifyEnv, "")
	t.Setenv(images.DockerCertPathEnv, "")
	t.Setenv(images.DockerHostEnv, "tcp://"+strings.TrimPrefix(daemon.URL, "http://"))
	return daemon
}
