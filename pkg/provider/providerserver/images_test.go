package providerserver_test

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"

	"google.golang.org/protobuf/proto"

	"github.com/ocelhq/ocel/pkg/images"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/progress"
	planv1 "github.com/ocelhq/ocel/pkg/proto/common/plan/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

const builtContentDigest = "sha256:18f506afda961821b6026743c6309340f89b9b842bf994d53c1d6e6119199165"

var pushedCoordinate = "ghcr.io/acme/shop.web:" + images.RuntimeTag(builtContentDigest, []byte(fake.RuntimeBinary), nil)

func registryDeployRequest() *contractv1.DeployRequest {
	return namingARegistry(containerDeployRequest("/"))
}

func encodingContains(t *testing.T, event proto.Message, text string) bool {
	t.Helper()
	encoded, err := proto.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	return bytes.Contains(encoded, []byte(text))
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

func (muteStacks) Plan(context.Context, provider.StackSpec, progress.Log) (provider.Plan, error) {
	return provider.Plan{}, nil
}

func (muteStacks) PlanDestroy(_ context.Context, ref provider.StackRef, _ progress.Log) (provider.Plan, error) {
	return provider.Plan{}, nil
}

func (muteStacks) Provision(_ context.Context, spec provider.StackSpec, _ progress.Log) (provider.StackResult, error) {
	return provider.StackResult{Containers: fake.ProvisionedContainers(spec)}, nil
}

func (muteStacks) Destroy(context.Context, provider.StackDestroy, progress.Log) error {
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

type recordReadingStacks struct {
	provider.Stacks
	store keyvalue.Store

	mu    sync.Mutex
	named []string
}

func (s *recordReadingStacks) Destroy(ctx context.Context, req provider.StackDestroy, progress progress.Log) error {
	ref := req.Ref
	recorded, _, err := stackrecords.Read(ctx, s.store, ref.Tier, ref.Project, ref.Name)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.named = append(s.named, recorded.Image)
	s.mu.Unlock()
	return s.Stacks.Destroy(ctx, req, progress)
}

func TestAFailedDeployDestroysItsAppStackWithARecordNamingTheImageItPushed(t *testing.T) {
	daemonWithTheBuiltImage(t, "amd64")
	builtProject(t)
	vendor := fake.NewProvider(fake.Options{Region: "nowhere"})
	vendor.FakeStacks().Entering(func(spec provider.StackSpec) error {
		if spec.Kind == provider.StackApp {
			return errors.New("the app stack failed")
		}
		return nil
	})
	reading := &recordReadingStacks{Stacks: vendor.Stacks(), store: vendor.KeyValues()}
	client := servedProvider(t, "1.0.0", refusingStacks{Provider: vendor, stacks: reading})
	bootstrappedOverRPC(t, client)

	if result, _ := deploy(t, client, registryDeployRequest()); result.GetSuccess() {
		t.Fatal("Deploy() succeeded, want the app stack to fail it")
	}

	reading.mu.Lock()
	defer reading.mu.Unlock()
	if !slices.Equal(reading.named, []string{pushedCoordinate}) {
		t.Errorf("the failed deploy destroyed stacks whose records name %q, want %q: the destroy reclaims only the image the record names, and nothing else records it", reading.named, pushedCoordinate)
	}
}

func TestTheSameAppInTwoProjectsIsPushedWhereOneProjectsRetentionNeverReachesTheOther(t *testing.T) {
	daemonWithTheBuiltImage(t, "amd64")
	builtProject(t)
	client, vendor := deployServed(t)

	for _, slug := range []string{"shop", "blog"} {
		req := registryDeployRequest()
		req.Manifest.Slug = slug
		if result, _ := deploy(t, client, req); result == nil || !result.GetSuccess() {
			t.Fatalf("Deploy() of %s = %q, want it to succeed", slug, result.GetError())
		}
	}

	pushed := vendor.ImageStore().Pushed()
	if len(pushed) != 2 {
		t.Fatalf("the deploys pushed %v, want one image per project although both run the same content", pushed)
	}
	shop, _, _ := strings.Cut(pushed[0].ImageRef, ":sha256-")
	blog, _, _ := strings.Cut(pushed[1].ImageRef, ":sha256-")
	if shop == blog {
		t.Errorf("projects shop and blog both pushed web to %s: retention sweeps an app's repository, so pruning one project would remove what the other still runs", shop)
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
	req.ProjectRegistry = &contractv1.ImageRegistry{Server: "ghcr.io", Namespace: "acme", Password: "hunter2"}
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
		if encodingContains(t, event, "hunter2") {
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

func (s refusingStore) ProbePush(context.Context, string) error { return nil }
func (s refusingStore) Remove(context.Context, string) error    { return nil }

func (s refusingStore) Has(context.Context, provider.ImagePush) (bool, error) {
	return false, nil
}

func (s refusingStore) Push(context.Context, provider.ImagePush, progress.Log) error {
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

var loadedCoordinate = "ocel/shop/web:" + images.RuntimeTag(builtContentDigest, []byte(fake.RuntimeBinary), nil)

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

func rebuiltContainerRequest() *contractv1.DeployRequest {
	req := containerDeployRequest("/")
	req.Manifest.Apps[0].Artifact = &contractv1.ManifestApp_Container{Container: &contractv1.ContainerArtifact{
		Image:           "ocel/shop/web@sha256:fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210",
		HealthCheckPath: "/",
		MinInstances:    1,
		MaxInstances:    1,
	}}
	return req
}

func TestARebuildOfUnchangedContentIsHeldByTheBoxThatHasTheImageItShipped(t *testing.T) {
	daemonWithTheBuiltImage(t, "amd64")
	builtProject(t)
	client, vendor := loadServed(t)
	vendor.direct.Preload(loadedCoordinate)

	req := rebuiltContainerRequest()
	req.Dry = true
	_, events := deploy(t, client, req)
	rows := imageRows(lastPlan(events))
	if len(rows) != 1 || rows[0].GetAction() != planv1.Change_ACTION_KEEP {
		t.Errorf("the plan shows %v for a rebuild of content the box already holds, want one %q row: the build's digest differs on every build, so a row keyed by it is never standing", rows, provider.ActionKeep)
	}

	result, _ := deploy(t, client, rebuiltContainerRequest())
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}
	if handed := vendor.direct.Pushed(); len(handed) != 0 {
		t.Errorf("the redeploy sent %v over a box that holds the same content under another build digest", handed)
	}
}

func TestARebuildOfUnchangedContentIsNotPushedAgainToARegistry(t *testing.T) {
	daemonWithTheBuiltImage(t, "amd64")
	builtProject(t)
	client, vendor := deployServed(t)
	vendor.ImageStore().Preload(pushedCoordinate)

	result, _ := deploy(t, client, namingARegistry(rebuiltContainerRequest()))
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}
	if pushed := vendor.ImageStore().Pushed(); len(pushed) != 0 {
		t.Errorf("the deploy pushed %v that the registry already holds under this content's digest", pushed)
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

	want := "Sending web's image to deploy@box.invalid (1 of 1)"
	for _, event := range events {
		if saidLine(event) == want {
			return
		}
	}
	t.Errorf("no event says %q: a transfer onto a machine reported as a push to %q names the coordinate where the reader expects the destination", want, loadedCoordinate)
}
