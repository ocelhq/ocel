package providerkit_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"

	planv1 "github.com/ocelhq/ocel/pkg/proto/common/plan/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/providerkit/fake"
	"github.com/ocelhq/ocel/pkg/providerkit/ledger"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const pushedCoordinate = "ghcr.io/acme/web:sha256-0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func registryDeployRequest() *contractv1.DeployRequest {
	return namingARegistry(containerDeployRequest("/"))
}

func imageRows(plan *planv1.ChangePlan) []*planv1.Change {
	var rows []*planv1.Change
	for _, group := range plan.GetGroups() {
		for _, change := range group.GetChanges() {
			if change.GetKind() == providerkit.ImageKind {
				rows = append(rows, change)
			}
		}
	}
	return rows
}

func TestADryDeployShowsTheImagePushAsARowAndPushesNothing(t *testing.T) {
	builtProject(t)
	client, provider := deployServed(t)

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
		t.Errorf("the image row says %q, want %q for a digest the registry does not hold", got, providerkit.ActionCreate)
	}
	registry := provider.Registry()
	if len(registry.Asked()) == 0 {
		t.Error("the plan drew an image row without asking the registry anything, so the row is guesswork rather than a diff")
	}
	if pushed := registry.Pushed(); len(pushed) != 0 {
		t.Errorf("a dry deploy pushed %v; preview diffs on the digest and pushes nothing", pushed)
	}
}

type muteReleaser struct{}

func (muteReleaser) Plan(context.Context, providerkit.StackPlan, providerkit.Reporter) (providerkit.Plan, error) {
	return providerkit.Plan{}, nil
}

func (muteReleaser) PlanDestroy(_ context.Context, ref providerkit.StackRef, _ providerkit.Reporter) (providerkit.Plan, error) {
	return providerkit.Plan{}, nil
}

func (muteReleaser) Provision(_ context.Context, plan providerkit.StackPlan, _ providerkit.Reporter) (providerkit.StackResult, error) {
	return providerkit.StackResult{Containers: fake.StoodUpContainers(plan)}, nil
}

func (muteReleaser) Destroy(context.Context, providerkit.StackRef, providerkit.Reporter) error {
	return nil
}

func TestTheImageRowIsTheReleasersOwnAndNothingElseInventsIt(t *testing.T) {
	builtProject(t)
	base := fake.NewProvider(fake.Options{Region: "nowhere"})
	client := servedBy(t, refusingReleaser{Provider: base, releaser: muteReleaser{}})
	standsBootstrapped(t, client)

	req := registryDeployRequest()
	req.Dry = true
	result, events := deploy(t, client, req)
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}

	if rows := imageRows(lastPlan(events)); len(rows) != 0 {
		t.Fatalf("the plan shows %d image rows over a releaser that declared none: a row nothing in the release program emitted cannot be verified by --dry or replayed", len(rows))
	}
}

func TestADeployPushesTheImageTheBuildProducedUnderTheRegistryCoordinate(t *testing.T) {
	builtProject(t)
	client, provider := deployServed(t)

	result, _ := deploy(t, client, registryDeployRequest())
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}

	pushed := provider.Registry().Pushed()
	if len(pushed) != 1 {
		t.Fatalf("the deploy pushed %v, want the one image its container app runs", pushed)
	}
	if pushed[0].Source != containerTestImage {
		t.Errorf("the push read %q from the local store, want the image the build produced, %q", pushed[0].Source, containerTestImage)
	}
	if pushed[0].Target != pushedCoordinate {
		t.Errorf("the push wrote %q, want %q", pushed[0].Target, pushedCoordinate)
	}
}

func TestADigestTheRegistryAlreadyHoldsIsNotPushedAgain(t *testing.T) {
	builtProject(t)
	client, provider := deployServed(t)
	provider.Registry().Holds(pushedCoordinate)

	result, _ := deploy(t, client, registryDeployRequest())
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}
	if pushed := provider.Registry().Pushed(); len(pushed) != 0 {
		t.Errorf("the deploy pushed %v that the registry already holds", pushed)
	}
}

func TestADigestTheRegistryAlreadyHoldsStandsOnThePlan(t *testing.T) {
	builtProject(t)
	client, provider := deployServed(t)
	provider.Registry().Holds(pushedCoordinate)

	req := registryDeployRequest()
	req.Dry = true
	_, events := deploy(t, client, req)

	rows := imageRows(lastPlan(events))
	if len(rows) != 1 || rows[0].GetAction() != planv1.Change_ACTION_KEEP {
		t.Errorf("the plan shows %v for an image the registry already holds, want one %q row", rows, providerkit.ActionKeep)
	}
}

func TestAContainerAppOnAProviderTakingNeitherPathFailsNamingTheGap(t *testing.T) {
	builtProject(t)
	client, provider := deployServed(t)

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
	if opened := provider.Registry().Opened(); len(opened) != 0 {
		t.Errorf("a registry was opened as %v where the deploy named none", opened)
	}
}

func TestAServerlessDeployNamesNoImageToPush(t *testing.T) {
	builtProject(t)
	client, provider := deployServed(t)

	req := deployRequest()
	req.ImageRegistry = &contractv1.ImageRegistry{Server: "ghcr.io", Namespace: "acme", Password: "hunter2"}
	result, _ := deploy(t, client, req)
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}
	if asked := provider.Registry().Asked(); len(asked) != 0 {
		t.Errorf("a serverless deploy asked the registry about %v, and it ships zips rather than images", asked)
	}
}

func TestARegistryThatCannotBeReachedStopsTheDeploy(t *testing.T) {
	builtProject(t)
	client, provider := deployServed(t)
	provider.Registry().Refusing(errors.New("the token is not accepted"))

	result, _ := deploy(t, client, registryDeployRequest())
	if result != nil && result.GetSuccess() {
		t.Fatal("Deploy() succeeded over a registry that refused the push, so the release would promote an image nothing can pull")
	}
	if !strings.Contains(result.GetError(), "the token is not accepted") {
		t.Errorf("Deploy() = %q, want the registry's own reason", result.GetError())
	}
}

func TestThePasswordTheDeployCarriesReachesTheRegistryAndNothingElse(t *testing.T) {
	builtProject(t)
	client, provider := deployServed(t)

	result, events := deploy(t, client, registryDeployRequest())
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}
	opened := provider.Registry().Opened()
	if len(opened) != 1 || opened[0].Password != "hunter2" {
		t.Fatalf("the registry was opened as %v, want the one target the deploy resolved", opened)
	}
	for _, event := range events {
		if strings.Contains(event.String(), "hunter2") {
			t.Fatalf("the deploy stream carries the registry password: %s", event.String())
		}
	}
}

func TestAnImageRowRidesInsideTheAppsOwnStackGroup(t *testing.T) {
	builtProject(t)
	client, _ := deployServed(t)

	req := registryDeployRequest()
	req.Dry = true
	_, events := deploy(t, client, req)

	for _, group := range lastPlan(events).GetGroups() {
		names := changeNames(group)
		if !slices.Contains(names, providerkit.ImageKind+":web") {
			continue
		}
		if group.GetKind() != providerkit.StackGroupKind || !strings.Contains(group.GetName(), "web") {
			t.Fatalf("the image row sits in the %s group %q, want the app's own stack, so an apply of that stack is what pushes it",
				group.GetKind(), group.GetName())
		}
		return
	}
	t.Fatal("no group carries the image row")
}

func TestTheImageStoreIsOpenedFromTheTargetTheDeployCarries(t *testing.T) {
	store := fake.NewImages()
	push := providerkit.ImagePush{App: "web", Source: containerTestImage, Target: pushedCoordinate}
	plan := providerkit.ImagePlan{Store: store, Pushes: []providerkit.ImagePush{push}}

	if err := plan.Ship(context.Background(), nil); err != nil {
		t.Fatalf("Ship() = %v", err)
	}
	if err := plan.Ship(context.Background(), nil); err != nil {
		t.Fatalf("Ship() = %v", err)
	}
	if pushed := store.Pushed(); len(pushed) != 1 {
		t.Errorf("Ship() pushed %d times, want the second run to find the digest already there", len(pushed))
	}
}

type refusingStore struct{ where string }

func (s refusingStore) ImageDestination() string { return s.where }

func (s refusingStore) Has(context.Context, providerkit.ImagePush) (bool, error) {
	return false, nil
}

func (s refusingStore) Push(context.Context, providerkit.ImagePush, providerkit.Reporter) error {
	return errors.New("the stream stopped short")
}

func TestATransferThatFailsNamesWhereItWasSendingRatherThanTheCoordinate(t *testing.T) {
	store := refusingStore{where: "box.invalid"}
	plan := providerkit.ImagePlan{Store: store, Pushes: []providerkit.ImagePush{{
		App: "web", Source: containerTestImage, Target: loadedCoordinate,
	}}}

	err := plan.Ship(context.Background(), nil)
	if err == nil {
		t.Fatal("Ship() = nil over a store that refuses every push")
	}
	if !strings.Contains(err.Error(), store.where) {
		t.Errorf("Ship() = %v, want the destination %q the deploy announced it was sending to", err, store.where)
	}
	if strings.Contains(err.Error(), loadedCoordinate) {
		t.Errorf("Ship() = %v: a direct transfer that failed reads as a push to a registry that was never involved", err)
	}
}

const loadedCoordinate = "ocel/web:sha256-0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

type loadingProvider struct {
	*fake.Provider
	direct *fake.Images
}

func (p loadingProvider) DirectImages(context.Context) (providerkit.ImageStore, error) {
	return p.direct, nil
}

func loadServed(t *testing.T) (contractv1connect.ProviderServiceClient, loadingProvider) {
	t.Helper()
	provider := loadingProvider{Provider: fake.NewProvider(fake.Options{Region: "nowhere"}), direct: fake.NewImages()}
	return servedBy(t, provider), provider
}

func TestAProviderThatTakesImagesDirectlyIsHandedTheOneTheBuildProduced(t *testing.T) {
	builtProject(t)
	client, provider := loadServed(t)

	result, _ := deploy(t, client, containerDeployRequest("/"))
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}

	handed := provider.direct.Pushed()
	if len(handed) != 1 {
		t.Fatalf("the deploy handed over %v, want the one image its container app runs: a box with no registry account is the zero-config default", handed)
	}
	if handed[0].Source != containerTestImage {
		t.Errorf("the transfer read %q from the local store, want %q", handed[0].Source, containerTestImage)
	}
	if handed[0].Target != loadedCoordinate {
		t.Errorf("the transfer landed %q, want the cli-owned coordinate %q verbatim, so release, rollback and retention never learn which path carried it",
			handed[0].Target, loadedCoordinate)
	}
	if pushed := provider.Registry().Pushed(); len(pushed) != 0 {
		t.Errorf("a deploy naming no registry pushed %v to one", pushed)
	}
}

func TestADigestTheBoxAlreadyHoldsIsNotSentAgain(t *testing.T) {
	builtProject(t)
	client, provider := loadServed(t)
	provider.direct.Holds(loadedCoordinate)

	result, _ := deploy(t, client, containerDeployRequest("/"))
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}
	if len(provider.direct.Asked()) == 0 {
		t.Error("the deploy streamed without asking whether the image was already there")
	}
	if handed := provider.direct.Pushed(); len(handed) != 0 {
		t.Errorf("the redeploy sent %v again over a box that already holds the digest", handed)
	}
}

func TestANamedRegistryTakesTheImageFromAProviderThatWouldOtherwiseLoadItDirectly(t *testing.T) {
	builtProject(t)
	client, provider := loadServed(t)

	result, _ := deploy(t, client, registryDeployRequest())
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}

	pushed := provider.Registry().Pushed()
	if len(pushed) != 1 || pushed[0].Target != pushedCoordinate {
		t.Fatalf("the deploy pushed %v, want the one registry coordinate %q", pushed, pushedCoordinate)
	}
	if handed := provider.direct.Pushed(); len(handed) != 0 {
		t.Errorf("the same deploy also carried %v straight onto the box: the registry setting is the only switch, and both paths ran", handed)
	}
	if asked := provider.direct.Asked(); len(asked) != 0 {
		t.Errorf("the direct store was asked about %v where a registry was named", asked)
	}
}

func TestADirectTransferStandsOnThePlanLikeAPush(t *testing.T) {
	builtProject(t)
	client, provider := loadServed(t)
	provider.direct.Holds(loadedCoordinate)

	req := containerDeployRequest("/")
	req.Dry = true
	_, events := deploy(t, client, req)

	rows := imageRows(lastPlan(events))
	if len(rows) != 1 || rows[0].GetAction() != planv1.Change_ACTION_KEEP {
		t.Errorf("the plan shows %v for an image the box already holds, want one %q row", rows, providerkit.ActionKeep)
	}
	if handed := provider.direct.Pushed(); len(handed) != 0 {
		t.Errorf("a dry deploy carried %v onto the box", handed)
	}
}

type addressedImages struct {
	*fake.Images
	at string
}

func (a addressedImages) ImageDestination() string { return a.at }

type addressingProvider struct {
	*fake.Provider
	direct addressedImages
}

func (p addressingProvider) DirectImages(context.Context) (providerkit.ImageStore, error) {
	return p.direct, nil
}

func TestTheDeploySaysWhereTheImageWentRatherThanWhatItIsCalledThere(t *testing.T) {
	builtProject(t)
	provider := addressingProvider{
		Provider: fake.NewProvider(fake.Options{Region: "nowhere"}),
		direct:   addressedImages{Images: fake.NewImages(), at: "deploy@box.invalid"},
	}
	client := servedBy(t, provider)

	result, events := deploy(t, client, containerDeployRequest("/"))
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}

	want := "Sending web's image to deploy@box.invalid"
	for _, event := range events {
		if strings.Contains(event.String(), want) {
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

func staging(t *testing.T, provider *fake.Provider) *stagingLedger {
	t.Helper()
	held := &stagingLedger{}
	provider.Edges().(*fake.Edges).Edge(fake.KindRelay).UseLedger(func(state edge.StackState) fake.Ledger {
		held.Ledger = ledger.New(provider.Records(), providerkit.Class(state.Class), state.Slug)
		return held
	})
	return held
}

func TestAnAppPlanNamesTheCoordinateTheProviderWillHoldRatherThanTheOneTheBuildLeft(t *testing.T) {
	builtProject(t)
	client, provider := deployServed(t)

	result, _ := deploy(t, client, registryDeployRequest())
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}

	plans := provider.Releases().(*fake.Releaser).Plans()
	app := plans[len(plans)-1].App
	if app == nil {
		t.Fatal("the last plan the releaser saw stands up no app")
	}
	if app.Image != pushedCoordinate {
		t.Errorf("Image = %q, want %q: what stands the app up is the coordinate the push wrote, and the local digest ref names nothing the runtime can reach", app.Image, pushedCoordinate)
	}
}

func TestTheStagedRecordNamesTheImageThatReleaseRuns(t *testing.T) {
	builtProject(t)
	client, provider := deployServed(t)
	held := staging(t, provider)

	result, _ := deploy(t, client, registryDeployRequest())
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}

	staged := held.records()
	if len(staged) != 1 {
		t.Fatalf("the deploy staged %d records, want the one app it released", len(staged))
	}
	if staged[0].Image != pushedCoordinate {
		t.Errorf("the staged record names the image %q, want %q: a promotion's unit is a build identity, and a rollback that re-runs a retained digest needs the record keyed by that identity to name the ref", staged[0].Image, pushedCoordinate)
	}
}

func TestAServerlessRecordNamesNoImage(t *testing.T) {
	builtProject(t)
	client, provider := deployServed(t)
	held := staging(t, provider)

	result, _ := deploy(t, client, deployRequest())
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}

	for _, record := range held.records() {
		if record.Image != "" {
			t.Errorf("%s staged the image %q, and a serverless release runs none", record.App, record.Image)
		}
	}
}
