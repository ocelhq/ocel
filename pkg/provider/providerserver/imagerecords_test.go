package providerserver_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/tarball"

	"github.com/ocelhq/ocel/pkg/arch"
	"github.com/ocelhq/ocel/pkg/containerimage"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/images"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/progress"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/provider/ledger"
	"github.com/ocelhq/ocel/pkg/router"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

type stagedLedger struct {
	t         *testing.T
	keyValues keyvalue.Store
}

func (l stagedLedger) records() []router.ReleaseRecord {
	l.t.Helper()
	var staged []router.ReleaseRecord
	for _, tier := range []environment.Tier{environment.TierProduction, environment.TierPreview} {
		entries, err := l.keyValues.List(context.Background(), ledger.Partition(tier, "shop"), "records")
		if err != nil {
			l.t.Fatalf("list the records the ledger staged in %s: %v", tier, err)
		}
		for _, entry := range entries {
			var record router.ReleaseRecord
			if err := json.Unmarshal(entry.Value, &record); err != nil {
				l.t.Fatalf("decode the record %s: %v", entry.Key, err)
			}
			staged = append(staged, record)
		}
	}
	return staged
}

func staging(t *testing.T, vendor *fake.Provider) stagedLedger {
	t.Helper()
	return stagedLedger{t: t, keyValues: vendor.KeyValues()}
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

func TestTheStagedRecordKeepsTheHealthPathTheProviderDiscoveredAndMarksItDiscovered(t *testing.T) {
	daemonWithTheBuiltImage(t, "amd64")
	builtProject(t)
	client, vendor := deployServed(t)
	stager := staging(t, vendor)

	result, _ := deploy(t, client, namingARegistry(containerDeployRequest("")))
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}

	staged := stager.records()
	if len(staged) != 1 {
		t.Fatalf("the deploy staged %d records, want the one app it released", len(staged))
	}
	if staged[0].HealthPath != fake.DiscoveredHealthPath || !staged[0].HealthPathDiscovered {
		t.Errorf("the staged record names the health path %q (discovered %t), want %q discovered: a later release gates on the path this one found", staged[0].HealthPath, staged[0].HealthPathDiscovered, fake.DiscoveredHealthPath)
	}
}

func TestAnAppThatNamesNoHealthPathIsHandedThePathItsLiveReleaseDiscovered(t *testing.T) {
	daemonWithTheBuiltImage(t, "amd64")
	builtProject(t)
	client, vendor := deployServed(t)

	if result, _ := deploy(t, client, namingARegistry(containerDeployRequest(""))); result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}
	next := namingARegistry(containerDeployRequest(""))
	next.Manifest.Apps[0].BuildId = "fedcba9876543210fedcba9876543210"
	if result, _ := deploy(t, client, next); result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}

	specs := vendor.FakeStacks().Provisioned()
	app := specs[len(specs)-1].App
	if app == nil {
		t.Fatal("the last spec the stacks port saw provisions no app")
	}
	if app.HealthCheckPath != "" || app.DiscoveredHealthCheckPath != fake.DiscoveredHealthPath {
		t.Errorf("the second release was handed health path %q and discovered path %q, want none and %q: a path the first release discovered stays the app's until it names one", app.HealthCheckPath, app.DiscoveredHealthCheckPath, fake.DiscoveredHealthPath)
	}
}

func TestAnAppThatNamesAHealthPathIsHandedNoDiscoveredOne(t *testing.T) {
	daemonWithTheBuiltImage(t, "amd64")
	builtProject(t)
	client, vendor := deployServed(t)
	stager := staging(t, vendor)

	if result, _ := deploy(t, client, namingARegistry(containerDeployRequest(""))); result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}
	next := namingARegistry(containerDeployRequest("/ready"))
	next.Manifest.Apps[0].BuildId = "fedcba9876543210fedcba9876543210"
	if result, _ := deploy(t, client, next); result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}

	specs := vendor.FakeStacks().Provisioned()
	if app := specs[len(specs)-1].App; app.DiscoveredHealthCheckPath != "" {
		t.Errorf("an app naming /ready was handed the discovered path %q, want none: a path the config names always wins", app.DiscoveredHealthCheckPath)
	}
	for _, record := range stager.records() {
		if record.HealthPath == "/ready" && record.HealthPathDiscovered {
			t.Errorf("the record for the release naming /ready marks its path discovered")
		}
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
	entries, err := stackrecords.List(context.Background(), vendor.KeyValues(), environment.TierProduction, "shop")
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
			_, _ = w.Write([]byte(`{"Id":"` + r.URL.Path + `","Created":"` + time.Now().Format(time.RFC3339Nano) + `","Architecture":"` + architecture + `","Os":"linux",` +
				`"Config":{"Entrypoint":["/app/server"]},"RootFS":{"Type":"layers","Layers":["sha256:aa"]}}`))
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
	return "ghcr.io/acme/shop.web:" + images.RuntimeTag(builtContentDigest, containerRuntimeBytes, nil)
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
	if len(asked) == 0 || slices.ContainsFunc(asked, func(push provider.ImagePush) bool { return push.ImageRef != asked[0].ImageRef }) {
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
	for _, app := range req.GetManifest().GetApps() {
		if container := app.GetContainer(); container != nil {
			container.Arch = arch.ARM64
		}
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

func (s *stubStore) ProbePush(context.Context, string) error { return nil }
func (s *stubStore) Remove(context.Context, string) error    { return nil }

func (s *stubStore) Push(_ context.Context, push provider.ImagePush, _ progress.Log) error {
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

func nextContainerRequest() *contractv1.DeployRequest {
	req := registryDeployRequest()
	req.Manifest.Apps[0].Framework = &contractv1.Framework{Name: "next"}
	return req
}

func TestANextContainerIsWrappedInItsProvidersNextServerRuntime(t *testing.T) {
	builtProject(t)
	daemonWithTheBuiltImage(t, "amd64")
	client, vendor := wrappingServed(t)
	vendor.ServingNextServer()

	result, _ := deploy(t, client, nextContainerRequest())
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}

	pushed := vendor.ImageStore().Pushed()
	if len(pushed) != 1 || pushed[0].Built == nil {
		t.Fatalf("the store was handed %v, want the wrapped image", pushed)
	}
	if env := configOf(t, pushed[0].Built).Env; slices.ContainsFunc(env, func(entry string) bool {
		return strings.HasPrefix(entry, "NEXT_ADAPTER_PATH=") || strings.HasPrefix(entry, "NODE_OPTIONS=")
	}) {
		t.Errorf("the pushed image has env %v, want the Next adapter path and node options left to the app and the container runtime", env)
	}
	next := &images.NextServerRuntime{Files: map[string][]byte{containerimage.NextServerPreloadFile: []byte(fake.NextServerPreload)}}
	if tag := "ghcr.io/acme/shop.web:" + images.RuntimeTag(builtContentDigest, containerRuntimeBytes, next); pushed[0].ImageRef != tag {
		t.Errorf("the image is pushed as %q, want %q: the tag names the Next runtime it carries", pushed[0].ImageRef, tag)
	}
}

func TestANextContainerOnAProviderWithoutANextServerRuntimeIsWrappedAsBefore(t *testing.T) {
	builtProject(t)
	daemonWithTheBuiltImage(t, "amd64")
	client, vendor := wrappingServed(t)

	result, _ := deploy(t, client, nextContainerRequest())
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}

	pushed := vendor.ImageStore().Pushed()
	if len(pushed) != 1 || pushed[0].Built == nil {
		t.Fatalf("the store was handed %v, want the wrapped image", pushed)
	}
	if pushed[0].ImageRef != wrappedCoordinate() {
		t.Errorf("the image is pushed as %q, want today's %q", pushed[0].ImageRef, wrappedCoordinate())
	}
}

var projectRegistry = &contractv1.ImageRegistry{Server: "registry.example.com", Namespace: "acme", Username: "acme-bot", Password: "hunter2"}

func TestRemoveProjectHandsEveryDestroyTheStoreOfTheRegistryTheProjectNames(t *testing.T) {
	client, vendor := deployedProject(t)
	req := projectRequest()
	req.ProjectRegistry = projectRegistry

	stream, err := client.RemoveProject(context.Background(), req)
	if err != nil {
		t.Fatalf("RemoveProject() error = %v", err)
	}
	if result, err := drain(stream); err != nil || !result.GetSuccess() {
		t.Fatalf("RemoveProject() = %q, %v", result.GetError(), err)
	}

	stores := vendor.FakeStacks().DestroyedWith()
	if len(stores) == 0 {
		t.Fatal("RemoveProject() destroyed no stack")
	}
	for _, store := range stores {
		if store != provider.ImageStore(vendor.ImageStore()) {
			t.Errorf("a stack was destroyed with %v, want the store opened on the project's registry: the images its deploys pushed there are deleted through it", store)
		}
	}
	opened := vendor.ImageStore().Opened()
	if len(opened) != 1 || opened[0].Server != "registry.example.com" || opened[0].Password != "hunter2" {
		t.Errorf("the removal opened %v, want the project's registry with the credentials the CLI sent, once", opened)
	}
}

func TestRemoveProjectWithoutARegistryOpensNoStore(t *testing.T) {
	client, vendor := deployedProject(t)

	stream, err := client.RemoveProject(context.Background(), projectRequest())
	if err != nil {
		t.Fatalf("RemoveProject() error = %v", err)
	}
	if result, err := drain(stream); err != nil || !result.GetSuccess() {
		t.Fatalf("RemoveProject() = %q, %v", result.GetError(), err)
	}

	for _, store := range vendor.FakeStacks().DestroyedWith() {
		if store != nil {
			t.Errorf("a stack was destroyed with %v, want none: no registry was named", store)
		}
	}
	if opened := vendor.ImageStore().Opened(); len(opened) != 0 {
		t.Errorf("the removal opened %v, want no registry", opened)
	}
}

func TestRemoveEnvironmentHandsEveryDestroyTheStoreOfTheRegistryTheProjectNames(t *testing.T) {
	t.Parallel()
	client, vendor := contractServed(t, "1.0.0")
	deployed(t, vendor, environment.TierPreview, "shop")
	seedPromotions(t, vendor, environment.TierPreview, "shop", "pr-7", "p1", "p2")
	outlived := naming.AppStack("pr-7", "web", releaseOf(t, releaseFor(7)))
	seedEnvironment(t, vendor, "shop", outlived, naming.InfraStack("pr-7"))

	stream, err := client.RemoveEnvironment(context.Background(), &contractv1.RemoveEnvironmentRequest{
		Slug:            "shop",
		Environment:     &environmentv1.Environment{Tier: environmentv1.Tier_TIER_PREVIEW, Identity: "pr-7"},
		ProjectRegistry: projectRegistry,
	})
	if err != nil {
		t.Fatalf("RemoveEnvironment() error = %v", err)
	}
	if result, err := drain(stream); err != nil || !result.GetSuccess() {
		t.Fatalf("RemoveEnvironment() = %q, %v", result.GetError(), err)
	}

	stores := vendor.FakeStacks().DestroyedWith()
	if len(stores) == 0 {
		t.Fatal("RemoveEnvironment() destroyed no stack")
	}
	for _, store := range stores {
		if store != provider.ImageStore(vendor.ImageStore()) {
			t.Errorf("a stack was destroyed with %v, want the store opened on the project's registry", store)
		}
	}
}

func TestADeployThatReclaimsADroppedBuildHandsTheDestroyTheStoreItPushedTo(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)
	seedKept(t, vendor)
	req := deployRequest()
	req.ProjectRegistry = projectRegistry

	if result, _ := deploy(t, client, req); !result.GetSuccess() {
		t.Fatalf("Deploy() = %q", result.GetError())
	}

	stores := vendor.FakeStacks().DestroyedWith()
	if len(stores) == 0 {
		t.Fatal("the deploy destroyed no stack, and this case drops a build past the retained promotions")
	}
	for _, store := range stores {
		if store != provider.ImageStore(vendor.ImageStore()) {
			t.Errorf("a dropped build was destroyed with %v, want the store the deploy pushed to: it is where that build's image lives", store)
		}
	}
}
