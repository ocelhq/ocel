package images_test

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/remote"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/providerkit/images"
)

func servingRegistry(t *testing.T) string {
	t.Helper()
	server := httptest.NewServer(registry.New())
	t.Cleanup(server.Close)
	return strings.TrimPrefix(server.URL, "http://")
}

func TestTheDaemonStoreRefusesAnImageItWasNeverHanded(t *testing.T) {
	push := images.Push{App: "server", ImageRef: "web-server:sha256-abc", Digest: "sha256:abc"}

	err := images.DaemonStore().Push(context.Background(), push, nil)
	if err == nil {
		t.Fatal("Push() took an image the deploy never built, want it refused: nothing was handed over to write")
	}
	if !strings.Contains(err.Error(), "server") {
		t.Errorf("Push() = %v, want it to name what it was asked to write", err)
	}
}

func TestTheDaemonStoreSaysSoWhenItCannotReachTheDaemon(t *testing.T) {
	t.Setenv("DOCKER_HOST", "tcp://127.0.0.1:1")
	push := images.Push{App: "server", ImageRef: "web-server:sha256-abc", Digest: "sha256:abc"}

	present, err := images.DaemonStore().Has(context.Background(), push)
	if err == nil {
		t.Fatalf("Has() = %v, nil against a daemon nothing answers on, want the failure surfaced: a deploy would take silence for an absent image and push over nothing", present)
	}
	if present {
		t.Error("Has() answered that a daemon it never reached has the image")
	}
}

func TestABuiltImageReachesTheRegistryWithoutADaemon(t *testing.T) {
	host := servingRegistry(t)
	dir := stagedFunc(t, map[string]string{
		"index.mjs":   "export const handler = () => {}",
		"config.json": functionConfig(t, nil),
	})
	image, err := images.FunctionImage(empty.Image, nodeRuntime, dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := image.Digest()
	if err != nil {
		t.Fatal(err)
	}
	target := images.Registry{Server: host, Namespace: "ocel"}
	push := images.Push{
		App:      "server",
		ImageRef: target.ImageRef("web-server", naming.DigestTag(digest.String())),
		Digest:   digest.String(),
		Built:    image,
	}

	store := images.RegistryStore(target)
	present, err := store.Has(context.Background(), push)
	if err != nil {
		t.Fatalf("Has() error = %v", err)
	}
	if present {
		t.Fatal("the registry answered that it already has an image nothing has pushed")
	}
	if err := store.Push(context.Background(), push, nil); err != nil {
		t.Fatalf("Push() error = %v", err)
	}

	ref, err := name.NewDigest(host+"/ocel/web-server@"+digest.String(), name.Insecure)
	if err != nil {
		t.Fatal(err)
	}
	pulled, err := remote.Image(ref)
	if err != nil {
		t.Fatalf("the registry has no image at %s after the push: %v", ref, err)
	}
	if got, err := pulled.Digest(); err != nil || got != digest {
		t.Errorf("the registry has %s at %s, want %s", got, ref, digest)
	}
	if present, err := store.Has(context.Background(), push); err != nil || !present {
		t.Errorf("Has() = %v, %v after the push, want the store to say the digest is already there", present, err)
	}
}
