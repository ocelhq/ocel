package bucket_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	connect "connectrpc.com/connect"

	"github.com/ocelhq/ocel/cli/internal/declare"
	"github.com/ocelhq/ocel/cli/internal/devstack/bucket"
	"github.com/ocelhq/ocel/cli/internal/devstack/docker/dockertest"
	blobv1 "github.com/ocelhq/ocel/pkg/proto/app/blob/v1"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
)

func declared(name string, origins ...string) declare.Resource {
	return declare.Resource{
		Name:   name,
		Type:   resourcesv1.ResourceType_RESOURCE_TYPE_BUCKET,
		Bucket: &resourcesv1.BucketConfig{AllowedOrigins: origins},
	}
}

func TestAnUploadBeforeAnyBucketIsDeclaredIsRefusedWithoutDocker(t *testing.T) {
	t.Parallel()

	engine := &dockertest.Engine{}
	component := bucket.New(engine.Opener(), nil, nil)

	_, err := component.PresignUpload(context.Background(), &blobv1.PresignUploadRequest{Bucket: "uploads"})

	var refused *connect.Error
	if !errors.As(err, &refused) || refused.Code() != connect.CodeFailedPrecondition {
		t.Fatalf("PresignUpload = %v, want %v", err, connect.CodeFailedPrecondition)
	}
	if len(engine.Specs) != 0 {
		t.Fatal("a container was started with no bucket declared")
	}
}

func TestTheEmulatorRunsPinnedLabelledAndOnAVolumeOfTheProject(t *testing.T) {
	t.Parallel()

	engine := &dockertest.Engine{}
	component := bucket.New(engine.Opener(), nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := component.Resolve(ctx, "shop-1a2b", []declare.Resource{declared("uploads")})
	if err == nil {
		t.Fatal("Resolve = nil against an emulator that is not there")
	}

	if len(engine.Specs) != 1 {
		t.Fatalf("ran %d containers, want the one emulator", len(engine.Specs))
	}
	spec := engine.Specs[0]
	if !strings.HasPrefix(spec.Image, "ghcr.io/ocelhq/floci:2.0.1-ocel.2@sha256:") {
		t.Errorf("Image = %q, want the ocelhq floci build pinned by digest", spec.Image)
	}
	if spec.Labels["dev.ocel.project"] != "shop-1a2b" || spec.Labels["dev.ocel.component"] != "bucket" {
		t.Errorf("Labels = %v, want the project and the component", spec.Labels)
	}
	if !strings.Contains(spec.Volume, "shop-1a2b") {
		t.Errorf("Volume = %q, want one named for the project", spec.Volume)
	}
	if len(engine.Stopped) != 1 {
		t.Errorf("stopped %v, want the emulator that never became ready", engine.Stopped)
	}
}
