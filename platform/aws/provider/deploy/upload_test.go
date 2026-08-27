package deploy

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/auto"
	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	sdk "github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/providerkit"
)

const bucketObjectType = "aws:s3/bucketObjectv2:BucketObjectv2"

type declaring struct {
	inner sdk.MockResourceMonitor

	mu       sync.Mutex
	declared []string
}

func (d *declaring) NewResource(args sdk.MockResourceArgs) (string, resource.PropertyMap, error) {
	d.mu.Lock()
	d.declared = append(d.declared, args.TypeToken+"::"+args.Name)
	d.mu.Unlock()
	return d.inner.NewResource(args)
}

func (d *declaring) Call(args sdk.MockCallArgs) (resource.PropertyMap, error) {
	return d.inner.Call(args)
}

func (d *declaring) names() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.Clone(d.declared)
}

func shipping(t *testing.T) providerkit.Upload {
	t.Helper()

	path := filepath.Join(t.TempDir(), "entry.zip")
	if err := os.WriteFile(path, []byte("a built function"), 0o644); err != nil {
		t.Fatal(err)
	}
	return providerkit.Upload{
		Name:   "server",
		Ref:    providerkit.ArtifactRef{Bucket: providerkit.StoreFunctions, Key: "functions/shop/web/server-abc123.zip"},
		Path:   path,
		Digest: "abc123",
	}
}

func shippingPlan(upload providerkit.Upload) providerkit.StackPlan {
	return providerkit.StackPlan{
		Ref:     providerkit.StackRef{Project: "conformance", Class: providerkit.ClassProduction, Name: naming.InfraStack("conformance")},
		Kind:    providerkit.StackInfra,
		Uploads: []providerkit.Upload{upload},
	}
}

func TestAnArtifactTheReleaseShipsIsAnEngineResourceInThePlan(t *testing.T) {
	t.Parallel()

	upload := shipping(t)
	engine := &mockedEngine{outputs: auto.OutputMap{}}
	planned, err := conformingReleaser(engine).Plan(context.Background(), shippingPlan(upload), nil)
	if err != nil {
		t.Fatalf("Plan() of a release shipping an artifact = %v", err)
	}

	want := naming.ResourceID(providerkit.UploadKind, upload.Name)
	for _, group := range planned.Groups {
		for _, change := range group.Changes {
			if change.Kind == bucketObjectType && change.Name == want {
				return
			}
		}
	}
	t.Errorf("the plan reads %+v, want the artifact the release ships among the engine's own rows", planned.Groups)
}

func TestAnArtifactTheReleaseShipsIsAnEngineResourceInTheApply(t *testing.T) {
	t.Parallel()

	upload := shipping(t)
	watcher := &declaring{inner: standInCloud{}}
	engine := &mockedEngine{outputs: auto.OutputMap{}, mocks: watcher}
	if _, err := conformingReleaser(engine).Provision(context.Background(), shippingPlan(upload), nil); err != nil {
		t.Fatalf("Provision() of a release shipping an artifact = %v", err)
	}

	want := bucketObjectType + "::" + naming.ResourceID(providerkit.UploadKind, upload.Name)
	if !slices.Contains(watcher.names(), want) {
		t.Errorf("the apply declared %v, want %q: the engine ships the artifact, never the deploy behind its back", watcher.names(), want)
	}
}
