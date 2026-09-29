package bucket_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	connect "connectrpc.com/connect"

	"github.com/ocelhq/ocel/cli/internal/declare"
	"github.com/ocelhq/ocel/cli/internal/devresources/bucket"
	"github.com/ocelhq/ocel/cli/internal/devresources/docker"
	"github.com/ocelhq/ocel/cli/internal/devresources/docker/dockertest"
	"github.com/ocelhq/ocel/pkg/constants"
	bucketv1 "github.com/ocelhq/ocel/pkg/proto/app/bucket/v1"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
)

func declared(name string, origins ...string) declare.Resource {
	return declare.Resource{
		Name:   name,
		Type:   resourcesv1.ResourceType_RESOURCE_TYPE_BUCKET,
		Bucket: &resourcesv1.BucketConfig{AllowedOrigins: origins},
	}
}

func noOrigins() []string { return nil }

func resolveInterrupted(t *testing.T, backend *bucket.Backend, project string) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := backend.Resolve(ctx, project, []declare.Resource{declared("uploads")}); err == nil {
		t.Fatal("Resolve = nil against a store that is not there")
	}
}

func secretKeyOf(t *testing.T, spec docker.Spec) string {
	t.Helper()
	for _, entry := range spec.Env {
		if key, found := strings.CutPrefix(entry, "RUSTFS_SECRET_KEY="); found {
			return key
		}
	}
	t.Fatalf("Env = %v, want the store's secret key set", spec.Env)
	return ""
}

func TestAnUploadBeforeAnyBucketIsDeclaredIsRefusedWithoutDocker(t *testing.T) {
	t.Parallel()

	engine := &dockertest.Engine{}
	backend := bucket.New(engine.OpenFunc(), t.TempDir(), noOrigins)

	_, err := backend.PresignUpload(context.Background(), &bucketv1.PresignUploadRequest{Bucket: "uploads"})

	var refused *connect.Error
	if !errors.As(err, &refused) || refused.Code() != connect.CodeFailedPrecondition {
		t.Fatalf("PresignUpload = %v, want %v", err, connect.CodeFailedPrecondition)
	}
	if len(engine.Specs) != 0 {
		t.Fatal("a container was started with no bucket declared")
	}
}

func TestTheStoreRunsPinnedLabelledOnAVolumeOfTheProjectAndStopsEvenIfInterrupted(t *testing.T) {
	t.Parallel()

	engine := &dockertest.Engine{}
	backend := bucket.New(engine.OpenFunc(), t.TempDir(), noOrigins)

	resolveInterrupted(t, backend, "shop-1a2b")

	if len(engine.Specs) != 1 {
		t.Fatalf("ran %d containers, want the one store", len(engine.Specs))
	}
	spec := engine.Specs[0]
	if spec.Image != constants.ObjectStoreImage() {
		t.Errorf("Image = %q, want the object store every vendor runs, pinned by digest", spec.Image)
	}
	if spec.Labels["dev.ocel.project"] != "shop-1a2b" || spec.Labels["dev.ocel.backend"] != "bucket" {
		t.Errorf("Labels = %v, want the project and the backend", spec.Labels)
	}
	if !strings.Contains(spec.Volume, "shop-1a2b") {
		t.Errorf("Volume = %q, want one named for the project", spec.Volume)
	}
	if err := backend.Close(context.Background(), true); err != nil {
		t.Fatalf("Close = %v", err)
	}
	if len(engine.Stopped) != 1 {
		t.Errorf("stopped %v, want the store that was running when the interrupt landed stopped by Close", engine.Stopped)
	}
}

func TestTheStoreKeyIsGeneratedOncePerProjectAndKeptPrivate(t *testing.T) {
	t.Parallel()

	stateDir := t.TempDir()
	first, second, other := &dockertest.Engine{}, &dockertest.Engine{}, &dockertest.Engine{}
	resolveInterrupted(t, bucket.New(first.OpenFunc(), stateDir, noOrigins), "shop-1a2b")
	resolveInterrupted(t, bucket.New(second.OpenFunc(), stateDir, noOrigins), "shop-1a2b")
	resolveInterrupted(t, bucket.New(other.OpenFunc(), t.TempDir(), noOrigins), "blog-3c4d")

	key := secretKeyOf(t, first.Specs[0])
	if len(key) < 32 || slices.Contains([]string{"test", "ocel", "minioadmin", "rustfsadmin"}, key) {
		t.Errorf("secret key = %q, want one generated for the project", key)
	}
	if again := secretKeyOf(t, second.Specs[0]); again != key {
		t.Errorf("a restarted dev run keyed the store %q, want the %q it kept", again, key)
	}
	if theirs := secretKeyOf(t, other.Specs[0]); theirs == key {
		t.Error("two projects share a store key")
	}

	entries, err := os.ReadDir(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		info, err := os.Stat(filepath.Join(stateDir, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm()&0o077 != 0 {
			t.Errorf("%s is %v, want it readable by its owner alone", entry.Name(), info.Mode().Perm())
		}
	}
}
