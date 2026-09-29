package vps_test

import (
	"context"
	"io"
	"log"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/registry"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
)

func heardAll(t *testing.T, progress *fake.Progress, want ...string) {
	t.Helper()
	lines := progress.Lines()
	for _, line := range want {
		if !slices.Contains(lines, line) {
			t.Errorf("progress never said %q:\n%s", line, strings.Join(lines, "\n"))
		}
	}
}

func TestProvisioningABucketNamesTheObjectStoreItLandsIn(t *testing.T) {
	t.Parallel()

	progress := &fake.Progress{}
	if _, err := over(&box{}).ProvisionBucket(context.Background(), aBucket(t, "uploads", false), progress); err != nil {
		t.Fatalf("ProvisionBucket() = %v", err)
	}
	heardAll(t, progress, "INFO Provisioning bucket uploads in object store shop-prod-infra-store-s3")
}

func TestProvisioningAPostgresNamesTheContainerItRunsIn(t *testing.T) {
	t.Parallel()

	machine := &box{}
	withRecordedPostgres(machine)
	progress := &fake.Progress{}
	if _, err := over(machine).ProvisionPostgres(context.Background(), aPostgres(t, "17"), progress); err != nil {
		t.Fatalf("ProvisionPostgres() = %v", err)
	}
	heardAll(t, progress, "INFO Provisioning postgres main in container shop-prod-web-r0a1b2c3d-main-pg")
}

func TestRemovingTheLastBucketSaysWhatItRemovedAndThatTheStoreGoesWithIt(t *testing.T) {
	t.Parallel()

	progress := &fake.Progress{}
	err := over(&box{kept: sealedRootKey()}).RemoveResource(context.Background(),
		provider.StackRef{Project: "shop", Tier: environment.TierProduction, Name: aStackName(t)}, bindingBucket(), progress)
	if err != nil {
		t.Fatalf("RemoveResource(bucket) = %v", err)
	}
	heardAll(t, progress,
		"INFO Removing bucket uploads and its objects from object store shop-prod-infra-store-s3",
		"INFO Removing object store shop-prod-infra-store-s3: no bucket in this environment uses it any more",
	)
}

func TestABucketWhoseStoreKeepsNoCredentialIsSaidToBeSkipped(t *testing.T) {
	t.Parallel()

	progress := &fake.Progress{}
	err := over(&box{}).RemoveResource(context.Background(),
		provider.StackRef{Project: "shop", Tier: environment.TierProduction, Name: aStackName(t)}, bindingBucket(), progress)
	if err != nil {
		t.Fatalf("RemoveResource(bucket) = %v", err)
	}
	heardAll(t, progress,
		"INFO Skipped removing bucket uploads: the box keeps no credential for object store shop-prod-infra-store-s3")
}

func TestRemovingAPostgresNamesTheContainerAndTheDataItTakes(t *testing.T) {
	t.Parallel()

	progress := &fake.Progress{}
	err := over(&box{}).RemoveResource(context.Background(),
		provider.StackRef{Project: "shop", Tier: environment.TierProduction, Name: aStackName(t)},
		provider.Binding{Type: provider.BindingPostgres, Name: "main"}, progress)
	if err != nil {
		t.Fatalf("RemoveResource(postgres) = %v", err)
	}
	heardAll(t, progress, "INFO Removing postgres main, its container shop-prod-web-r0a1b2c3d-main-pg and its data")
}

func TestStartingAndRemovingAnAppNameItsContainer(t *testing.T) {
	t.Parallel()

	progress := &fake.Progress{}
	started, err := over(&box{}).ProvisionContainers(context.Background(), aStack(t, anApp()), progress)
	if err != nil {
		t.Fatalf("ProvisionContainers() = %v", err)
	}
	if err := over(&box{}).RemoveContainers(context.Background(), provider.StackRef{}, started, progress); err != nil {
		t.Fatalf("RemoveContainers() = %v", err)
	}
	heardAll(t, progress,
		"INFO Starting web's container "+started[0].Physical,
		"INFO Removing web's container "+started[0].Physical,
	)
}

func TestAnImagePulledOntoTheBoxEchoesDockersOutputOneLineAtATime(t *testing.T) {
	t.Parallel()

	machine := &box{}
	served := httptest.NewServer(registry.New(registry.Logger(log.New(io.Discard, "", 0))))
	t.Cleanup(served.Close)
	server := strings.TrimPrefix(served.URL, "http://")
	store, err := over(machine).OpenRegistryImages(context.Background(), provider.RegistryTarget{Server: server})
	if err != nil {
		t.Fatal(err)
	}
	ref := server + "/shop/web:sha256-abc-ocel-0123"
	progress := &fake.Progress{}
	if err := store.Push(context.Background(), provider.ImagePush{App: "web", Source: "ocel/shop/web@sha256:abc", ImageRef: ref, Built: wrapped(t)}, progress); err != nil {
		t.Fatalf("Push() = %v", err)
	}
	heardAll(t, progress, "OUTPUT sha256-abc: Pulling from shop/web", "OUTPUT Digest: sha256:abc")
	if !slices.ContainsFunc(progress.Lines(), func(line string) bool {
		return strings.HasPrefix(line, "OUTPUT Status: Downloaded newer image for "+server+"/shop/web@sha256:")
	}) {
		t.Errorf("the pull never echoed docker's status line on its own:\n%s", strings.Join(progress.Lines(), "\n"))
	}
}

func TestAnImageLoadedOntoTheBoxEchoesWhatDockerLoaded(t *testing.T) {
	daemonServing(t, "tar-bytes")
	progress := &fake.Progress{}
	if err := directImagesOn(t, &box{}).Push(context.Background(), aPush(t), progress); err != nil {
		t.Fatalf("Push() = %v", err)
	}
	heardAll(t, progress, "OUTPUT Loaded image: "+loadedImageRef)
}
