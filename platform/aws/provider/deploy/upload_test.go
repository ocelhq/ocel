package deploy

import (
	"context"
	"slices"
	"sync"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	sdk "github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider/functioncode"
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

func TestAFunctionTheReleaseShipsIsAnEngineResourceInThePlan(t *testing.T) {
	t.Parallel()

	engine := &mockedEngine{outputs: siblingAppOutputs("web")}
	planned, err := stacksWith(siblingAppConfig(t, "web"), engine).Plan(context.Background(), siblingAppSpec(t, "web"), nil)
	if err != nil {
		t.Fatalf("Plan() of a release shipping a function = %v", err)
	}

	want := naming.ResourceID(functioncode.Kind, "fn--web--entry")
	for _, group := range planned.Groups {
		for _, change := range group.Changes {
			if change.Kind == bucketObjectType && change.Name == want {
				return
			}
		}
	}
	t.Errorf("the plan reads %+v, want the function package the release ships among the engine's own rows", planned.Groups)
}

func TestAFunctionTheReleaseShipsIsAnEngineResourceInTheApply(t *testing.T) {
	t.Parallel()

	watcher := &declaring{inner: standInCloud{}}
	engine := &mockedEngine{outputs: siblingAppOutputs("web"), mocks: watcher}
	if _, err := stacksWith(siblingAppConfig(t, "web"), engine).Provision(context.Background(), siblingAppSpec(t, "web"), nil); err != nil {
		t.Fatalf("Provision() of a release shipping a function = %v", err)
	}

	want := bucketObjectType + "::" + naming.ResourceID(functioncode.Kind, "fn--web--entry")
	if !slices.Contains(watcher.names(), want) {
		t.Errorf("the apply declared %v, want %q: the engine ships the function package, never the deploy behind its back", watcher.names(), want)
	}
}
