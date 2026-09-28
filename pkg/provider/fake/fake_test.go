package fake_test

import (
	"bytes"
	"context"
	"slices"
	"testing"

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

func TestArtifactsRemovePrefixLeavesTheRest(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	artifacts := fake.NewArtifacts()
	kept := provider.ArtifactRef{Tier: environment.TierProduction, Bucket: provider.StoreFunctions, Key: "other/app.zip"}
	removed := provider.ArtifactRef{Tier: environment.TierProduction, Bucket: provider.StoreAssets, Key: "releases/r1/app.zip"}

	for _, ref := range []provider.ArtifactRef{kept, removed} {
		if err := artifacts.Put(ctx, ref, bytes.NewReader([]byte("body"))); err != nil {
			t.Fatalf("Put() error = %v", err)
		}
	}

	if err := artifacts.RemovePrefix(ctx, environment.TierProduction, "releases/", nil); err != nil {
		t.Fatalf("RemovePrefix() error = %v", err)
	}
	if _, err := artifacts.Open(ctx, removed); err == nil {
		t.Error("Open() found an artifact under the removed prefix")
	}
	if _, err := artifacts.Open(ctx, kept); err != nil {
		t.Errorf("Open() of an artifact outside the prefix: error = %v", err)
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

func TestTheReferenceProviderIsReachedThroughThePrimitiveItsAppsComputeNames(t *testing.T) {
	t.Parallel()

	p := fake.NewProvider(fake.Options{})
	stacks := resources.NewHookStacks(p.KeyValues(), p.Artifacts(), p.ResourceHooks())
	ref := provider.StackRef{
		Project: "shop",
		Tier:    environment.TierProduction,
		Name:    naming.AppStack("prod", "web", naming.NewRelease("d1", "f1")),
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
	progress := &fake.Progress{}
	artifacts := fake.NewArtifacts()
	stacks := fake.NewStacks(artifacts)
	bootstrap := fake.NewBootstrap()
	ref := provider.StackRef{Project: "shop", Tier: environment.TierProduction, Name: naming.InfraStack("prod")}

	if _, err := stacks.Provision(ctx, provider.StackSpec{Ref: ref, Kind: provider.StackInfra}, progress); err != nil {
		t.Fatalf("Provision() = %v", err)
	}
	if err := stacks.Destroy(ctx, ref, progress); err != nil {
		t.Fatalf("Destroy() = %v", err)
	}
	if err := bootstrap.Apply(ctx, provider.BootstrapRequest{Tier: environment.TierProduction}, progress); err != nil {
		t.Fatalf("Apply() = %v", err)
	}
	if err := bootstrap.Remove(ctx, environment.TierProduction, progress); err != nil {
		t.Fatalf("Remove() = %v", err)
	}
	if err := artifacts.RemovePrefix(ctx, environment.TierProduction, "releases/", progress); err != nil {
		t.Fatalf("RemovePrefix() = %v", err)
	}

	stack := naming.InfraStack("prod").String()
	want := []string{
		"INFO Provisioned stack " + stack,
		"INFO Destroyed stack " + stack,
		"INFO Applied the production bootstrap",
		"INFO Removed the production bootstrap",
		"INFO Removed the production artifacts under releases/",
	}
	if got := progress.Lines(); !slices.Equal(got, want) {
		t.Errorf("the reference provider said %q, want %q", got, want)
	}
}
