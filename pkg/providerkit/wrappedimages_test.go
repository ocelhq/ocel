package providerkit_test

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/tarball"

	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/providerkit/fake"
)

var containerRuntimeBytes = []byte("the ocel container runtime")

type daemonHolding struct {
	mu       sync.Mutex
	exported int
}

func (d *daemonHolding) exports() int {
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

func daemonHoldingTheBuiltImage(t *testing.T, arch string) *daemonHolding {
	t.Helper()
	saved := savedImage(t)
	held := &daemonHolding{}
	daemonServing(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/json"):
			_, _ = w.Write([]byte(`{"Architecture":"` + arch + `","Os":"linux"}`))
		case strings.HasSuffix(r.URL.Path, "/get"):
			held.mu.Lock()
			held.exported++
			held.mu.Unlock()
			_, _ = w.Write(saved)
		default:
			http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotFound)
		}
	})
	return held
}

func wrappingServed(t *testing.T) (contractv1connect.ProviderServiceClient, *fake.Provider) {
	t.Helper()
	return wrappingServedOn(t, "amd64")
}

func wrappingServedOn(t *testing.T, arch string) (contractv1connect.ProviderServiceClient, *fake.Provider) {
	t.Helper()
	provider := fake.NewProvider(fake.Options{Region: "nowhere"})
	client := servedBy(t, provider.WrappingContainers(arch, containerRuntimeBytes))
	return client, provider
}

func wrappedCoordinate() string {
	_, digest, _ := strings.Cut(containerTestImage, "@")
	return "ghcr.io/acme/web:" + providerkit.RuntimeTag(digest, containerRuntimeBytes)
}

func TestAWrappingProviderPushesTheImageUnderTheCoordinateTheRuntimeItCarriesNames(t *testing.T) {
	builtProject(t)
	daemon := daemonHoldingTheBuiltImage(t, "amd64")
	client, provider := wrappingServed(t)

	result, _ := deploy(t, client, registryDeployRequest())
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}

	asked := provider.Registry().Asked()
	if len(asked) != 1 {
		t.Fatalf("the deploy asked the registry about %v, want the one image its container app runs", asked)
	}
	if asked[0].ImageRef != wrappedCoordinate() {
		t.Errorf("the deploy asked about %q, want %q: a wrapped image is reached under a tag naming the runtime it boots through", asked[0].ImageRef, wrappedCoordinate())
	}
	if asked[0].Wrap == nil {
		t.Error("the push carries no wrap, so the image would reach the registry without the runtime the tag promises")
	}
	if asked[0].Digest != "" {
		t.Errorf("the push pins %q before the wrap has run, and the digest it is pushed under is only known once the image is built", asked[0].Digest)
	}
	pushed := provider.Registry().Pushed()
	if len(pushed) != 1 || pushed[0].Built == nil {
		t.Fatalf("the store was handed %v, want the wrapped image itself rather than a coordinate to copy", pushed)
	}
	if daemon.exports() != 1 {
		t.Errorf("the deploy read the image out of the daemon %d times, want the one export the wrap needs", daemon.exports())
	}
}

func TestTheArchitectureTheDaemonNamesIsWhatTheRuntimeIsAskedFor(t *testing.T) {
	builtProject(t)
	daemonHoldingTheBuiltImage(t, "arm64")
	client, provider := wrappingServedOn(t, "arm64")

	result, _ := deploy(t, client, registryDeployRequest())
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}

	if asked := provider.WrappedFor(); len(asked) == 0 || asked[0] != "arm64" {
		t.Errorf("the provider was asked for a runtime built for %v, want the architecture the daemon says the image is built for: a runtime built for another one cannot execute", asked)
	}
}

func TestAnImageBuiltForAnArchitectureTheTargetDoesNotRunIsRefusedBeforeItIsPushed(t *testing.T) {
	builtProject(t)
	daemon := daemonHoldingTheBuiltImage(t, "arm64")
	client, provider := wrappingServedOn(t, "amd64")

	_, _, err := deployStream(t, client, registryDeployRequest())
	if err == nil {
		t.Fatal("Deploy() succeeded, want it refused: the target cannot execute an image built for another architecture")
	}
	for _, named := range []string{"linux/arm64", "linux/amd64"} {
		if !strings.Contains(err.Error(), named) {
			t.Errorf("Deploy() refused with %q, want it to name %s so the mismatch reads at a glance", err, named)
		}
	}
	if pushed := provider.Registry().Pushed(); len(pushed) != 0 {
		t.Errorf("the store was handed %v, want nothing pushed for an image the target cannot run", pushed)
	}
	if daemon.exports() != 0 {
		t.Errorf("the deploy read the image out of the daemon %d times, want none for an image it refuses", daemon.exports())
	}
}

func TestAnAppsOwnDeclaredArchitectureIsTheOneItsImageIsHeldTo(t *testing.T) {
	builtProject(t)
	daemonHoldingTheBuiltImage(t, "arm64")
	client, provider := wrappingServedOn(t, "amd64")

	req := registryDeployRequest()
	for _, container := range req.GetManifest().GetContainers() {
		container.Arch = providerkit.ArchARM64
	}
	result, _ := deploy(t, client, req)
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed: the app declares arm64 and its image is built for it", result.GetError())
	}
	if asked := provider.WrappedFor(); len(asked) == 0 || asked[0] != "arm64" {
		t.Errorf("the provider was asked for a runtime built for %v, want arm64", asked)
	}
}

func TestAWrappedContainerRunsTheCoordinateItWasPushedUnder(t *testing.T) {
	builtProject(t)
	daemonHoldingTheBuiltImage(t, "amd64")
	client, provider := wrappingServed(t)

	result, _ := deploy(t, client, registryDeployRequest())
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}

	plans := provider.FakeStacks().Plans()
	var image string
	for i := len(plans) - 1; i >= 0; i-- {
		if plans[i].App != nil {
			image = plans[i].App.Image
			break
		}
	}
	if image != wrappedCoordinate() {
		t.Errorf("the app plan runs %q, want %q: the box pulls the wrapped image rather than the one the build produced", image, wrappedCoordinate())
	}
}

func TestAWrappedCoordinateTheRegistryAlreadyHoldsIsNeitherWrappedNorPushed(t *testing.T) {
	builtProject(t)
	daemon := daemonHoldingTheBuiltImage(t, "amd64")
	client, provider := wrappingServed(t)
	provider.Registry().Holds(wrappedCoordinate())

	result, _ := deploy(t, client, registryDeployRequest())
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}

	if pushed := provider.Registry().Pushed(); len(pushed) != 0 {
		t.Errorf("the deploy pushed %v that the registry already holds", pushed)
	}
	if daemon.exports() != 0 {
		t.Errorf("the deploy exported the image %d times for a coordinate the registry already holds: wrapping reads the whole image off the daemon, and nothing is going to be pushed", daemon.exports())
	}
}

type stubStore struct {
	held   bool
	pushed []providerkit.ImagePush
}

func (s *stubStore) Has(context.Context, providerkit.ImagePush) (bool, error) {
	return s.held, nil
}

func (s *stubStore) Destination() string { return "the stub registry" }

func (s *stubStore) Push(_ context.Context, push providerkit.ImagePush, _ providerkit.Progress) error {
	s.pushed = append(s.pushed, push)
	return nil
}

func TestShipHandsTheStoreTheWrappedImageAndClearsUpAfterIt(t *testing.T) {
	t.Parallel()

	store := &stubStore{}
	cleaned := false
	plan := providerkit.ImagePlan{Store: store, Pushes: []providerkit.ImagePush{{
		App:      "web",
		ImageRef: "ghcr.io/acme/web:sha256-abc-ocel-0123456789ab",
		Wrap: func(context.Context) (v1.Image, func(), error) {
			return empty.Image, func() { cleaned = true }, nil
		},
	}}}

	if err := plan.Ship(context.Background(), nil); err != nil {
		t.Fatalf("Ship() = %v", err)
	}
	if len(store.pushed) != 1 || store.pushed[0].Built != empty.Image {
		t.Fatalf("the store was handed %v, want the image the wrap built", store.pushed)
	}
	if !cleaned {
		t.Error("the wrap's own cleanup never ran, so every wrapped push leaves the exported image behind on disk")
	}
}

func TestShipRunsNoWrapForACoordinateTheStoreAlreadyHolds(t *testing.T) {
	t.Parallel()

	store := &stubStore{held: true}
	plan := providerkit.ImagePlan{Store: store, Pushes: []providerkit.ImagePush{{
		App:      "web",
		ImageRef: "ghcr.io/acme/web:sha256-abc-ocel-0123456789ab",
		Wrap: func(context.Context) (v1.Image, func(), error) {
			return nil, nil, errors.New("the image was wrapped for a push that was never needed")
		},
	}}}

	if err := plan.Ship(context.Background(), nil); err != nil {
		t.Fatalf("Ship() = %v", err)
	}
	if len(store.pushed) != 0 {
		t.Errorf("the store was handed %v for a coordinate it already holds", store.pushed)
	}
}
