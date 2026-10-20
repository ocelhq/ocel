package fake_test

import (
	"bytes"
	"context"
	"slices"
	"testing"

	"github.com/ocelhq/ocel/pkg/containerimage"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/conformance"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/provider/resources"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

func TestTheFakeKeyValueStoreConformsAsEveryStoreMust(t *testing.T) {
	conformance.RunStore(t, fake.NewKeyValues())
}

func TestArtifactsRemoveObjectsLeavesTheRest(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	artifacts := fake.NewArtifacts()
	scope, err := naming.AppScope(naming.Coordinate{Project: "shop", Env: "pr-7", App: "web"})
	if err != nil {
		t.Fatal(err)
	}
	kept := provider.ArtifactRef{Tier: environment.TierProduction, Key: "fn/shop/prod/web/r1a2b3c4d/api/1.zip"}
	removed := provider.ArtifactRef{Tier: environment.TierProduction, Key: "fn/shop/pr-7/web/r1a2b3c4d/api/1.zip"}
	otherKind := provider.ArtifactRef{Tier: environment.TierProduction, Key: "assets/shop/pr-7/web/r1a2b3c4d/app.js"}

	for _, ref := range []provider.ArtifactRef{kept, removed, otherKind} {
		if err := artifacts.Put(ctx, ref, bytes.NewReader([]byte("body"))); err != nil {
			t.Fatalf("Put() error = %v", err)
		}
	}

	if err := artifacts.RemoveObjects(ctx, environment.TierProduction, scope, []naming.ObjectKind{naming.ObjectFunction}, nil); err != nil {
		t.Fatalf("RemoveObjects() error = %v", err)
	}
	if _, err := artifacts.Open(ctx, removed); err == nil {
		t.Error("Open() found an artifact under the removed scope")
	}
	for _, ref := range []provider.ArtifactRef{kept, otherKind} {
		if _, err := artifacts.Open(ctx, ref); err != nil {
			t.Errorf("Open(%s) of an artifact outside the scope or kinds removed: error = %v", ref.Key, err)
		}
	}
}

func TestNewRefusesOptionsTheReferenceProviderDoesNotAccept(t *testing.T) {
	t.Parallel()

	if _, err := fake.New(context.Background(), provider.Settings{Options: provider.Options{"regoin": "typo"}}); err == nil {
		t.Fatal("New() accepted an option it does not know")
	}
	if _, err := fake.New(context.Background(), provider.Settings{Options: provider.Options{"region": "nowhere"}}); err != nil {
		t.Fatalf("New() error = %v", err)
	}
}

func TestAFakeProviderForAProjectShipsANextServerRuntimeWithItsPreload(t *testing.T) {
	t.Parallel()

	files, err := fake.NewForProject(fake.Options{}).Hooks().NextServerRuntime(context.Background())
	if err != nil {
		t.Fatalf("NextServerRuntime() error = %v", err)
	}
	if got := string(files[containerimage.NextServerPreloadFile]); got != fake.NextServerPreload {
		t.Errorf("ReadNextServerRuntime()[%s] = %q, want %q", containerimage.NextServerPreloadFile, got, fake.NextServerPreload)
	}
	if bare := fake.NewProvider(fake.Options{}).Hooks().NextServerRuntime; bare != nil {
		t.Errorf("a bare fake provider ships a Next server runtime, want none")
	}
}

func TestTheReferenceProviderIsReachedThroughThePrimitiveItsAppsComputeNames(t *testing.T) {
	t.Parallel()

	p := fake.NewProvider(fake.Options{})
	stacks := resources.NewHookStacks(p.KeyValues(), p.ResourceHooks())
	ref := provider.StackRef{
		Project: "shop",
		Tier:    environment.TierProduction,
		Name:    naming.AppStack("prod", "web", naming.NewReleaseToken("d1", "f1")),
	}

	served, err := stacks.Provision(context.Background(), provider.StackSpec{
		Ref:  ref,
		Kind: provider.StackApp,
		App: &provider.AppSpec{
			App:       "web",
			Compute:   provider.ComputeServerless,
			Functions: []provider.FunctionSpec{{Name: "api"}},
		},
	}, nil)
	if err != nil {
		t.Fatalf("Provision() of a serverless app = %v", err)
	}
	if len(served.Functions) != 1 || len(served.Containers) != 0 {
		t.Fatalf("Provision() of a serverless app = %+v, want it to reach Functions alone", served)
	}

	contained, err := stacks.Provision(context.Background(), provider.StackSpec{
		Ref:  ref,
		Kind: provider.StackApp,
		App: &provider.AppSpec{
			App:             "web",
			Compute:         provider.ComputeContainer,
			Image:           "ocel/web@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
			HealthCheckPath: "/",
		},
	}, nil)
	if err != nil {
		t.Fatalf("Provision() of a container app = %v", err)
	}
	if len(contained.Containers) != 1 || len(contained.Functions) != 0 {
		t.Fatalf("Provision() of a container app = %+v, want it to reach the Containers hooks alone", contained)
	}

	if err := stackrecords.Write(context.Background(), p.KeyValues(), ref.Tier, ref.Project, ref.Name, stackrecords.Stack{
		Kind:       provider.StackApp,
		Containers: contained.Containers,
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := stacks.Provision(context.Background(), provider.StackSpec{
		Ref:  ref,
		Kind: provider.StackApp,
		App: &provider.AppSpec{
			App:       "web",
			Compute:   provider.ComputeServerless,
			Functions: []provider.FunctionSpec{{Name: "api"}},
		},
	}, nil); err != nil {
		t.Fatalf("Provision() of an app moving back to serverless = %v", err)
	}
	if taken := p.FakeStacks().Destroyed(); !slices.Contains(taken, "web") {
		t.Errorf("the reference provider took down %v, want the container the app left behind: an app changing compute leaves the other primitive's work running otherwise", taken)
	}
}

func TestTheReferenceProviderSaysWhatItDidAndToWhichStackTierOrPrefix(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	progress := &fake.Log{}
	artifacts := fake.NewArtifacts()
	stacks := fake.NewStacks(artifacts)
	bootstrap := fake.NewBootstrap()
	ref := provider.StackRef{Project: "shop", Tier: environment.TierProduction, Name: naming.InfraStack("prod")}

	if _, err := stacks.Provision(ctx, provider.StackSpec{Ref: ref, Kind: provider.StackInfra}, progress); err != nil {
		t.Fatalf("Provision() = %v", err)
	}
	if err := stacks.Destroy(ctx, provider.StackDestroy{Ref: ref}, progress); err != nil {
		t.Fatalf("Destroy() = %v", err)
	}
	if err := bootstrap.Apply(ctx, provider.BootstrapRequest{Tier: environment.TierProduction}, progress); err != nil {
		t.Fatalf("Apply() = %v", err)
	}
	if err := bootstrap.Remove(ctx, environment.TierProduction, progress); err != nil {
		t.Fatalf("Remove() = %v", err)
	}
	shop, err := naming.ProjectScope("shop")
	if err != nil {
		t.Fatal(err)
	}
	if err := artifacts.RemoveObjects(ctx, environment.TierProduction, shop, []naming.ObjectKind{naming.ObjectFunction, naming.ObjectAssets}, progress); err != nil {
		t.Fatalf("RemoveObjects() = %v", err)
	}

	stack := naming.InfraStack("prod").String()
	want := []string{
		"INFO Provisioned stack " + stack,
		"INFO Destroyed stack " + stack,
		"INFO Applied the production bootstrap",
		"INFO Removed the production bootstrap",
		"INFO Removed the production objects under fn/shop/, assets/shop/",
	}
	if got := progress.Lines(); !slices.Equal(got, want) {
		t.Errorf("the reference provider said %q, want %q", got, want)
	}
}
