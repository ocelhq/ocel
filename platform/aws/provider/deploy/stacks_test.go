package deploy

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/provider/pulumi"
	"github.com/ocelhq/ocel/pkg/records"
)

func inventoriedStacks(t *testing.T, exported map[string][]pulumi.ExportedResource) (*Stacks, Config, provider.StackSpec) {
	t.Helper()
	return inventoriedStacksIn(t, fake.NewKeyValues(), exported)
}

func inventoriedStacksIn(t *testing.T, store records.Store, exported map[string][]pulumi.ExportedResource) (*Stacks, Config, provider.StackSpec) {
	t.Helper()
	cfg, spec := appStackSpec(t)
	cfg.BackendURL = "s3://ocel-state/shop"
	cfg.Passphrase = "a-passphrase"
	cfg.PulumiProject = "ocel-shop"
	cfg.Region = "eu-west-1"
	cfg.KeyValues = store
	return stacksWith(cfg, &mockedEngine{exported: exported}), cfg, spec
}

type unlistableStore struct {
	records.Store
}

func (unlistableStore) List(context.Context, records.Partition, ...string) ([]records.Entry, error) {
	return nil, errors.New("listed a partition")
}

func TestStackResourcesNameEveryResourceThePulumiStateHoldsByItsCloudFormationType(t *testing.T) {
	t.Parallel()

	stacks, _, spec := inventoriedStacks(t, nil)
	stacks.engine.(*mockedEngine).exported = map[string][]pulumi.ExportedResource{spec.Ref.Name.String(): {
		{Type: "aws:lambda/function:Function", ID: "shop-prod-web-entry"},
		{Type: "aws:iam/role:Role", ID: "shop-prod-web-role"},
		{Type: "ocel:index:AssetSet", ID: "shop-prod-web-assets"},
	}}

	listed, err := stacks.Resources(context.Background(), provider.StackResources{Ref: spec.Ref})
	if err != nil {
		t.Fatalf("Resources() = %v", err)
	}

	want := []provider.PhysicalResource{
		{Kind: "AWS::Lambda::Function", ID: "shop-prod-web-entry", Location: "eu-west-1"},
		{Kind: "AWS::IAM::Role", ID: "shop-prod-web-role", Location: "global"},
		{Kind: "ocel:index:AssetSet", ID: "shop-prod-web-assets", Location: "eu-west-1"},
	}
	if !slices.Equal(listed, want) {
		t.Errorf("Resources() = %+v, want %+v", listed, want)
	}
}

func TestStackResourcesOfAStackNeverProvisionedAreNone(t *testing.T) {
	t.Parallel()

	stacks, _, spec := inventoriedStacks(t, map[string][]pulumi.ExportedResource{
		"prod--infra": {{Type: "aws:sqs/queue:Queue", ID: "https://sqs.eu-west-1.amazonaws.com/123456789012/shop-prod-jobs"}},
	})

	listed, err := stacks.Resources(context.Background(), provider.StackResources{Ref: spec.Ref})
	if err != nil || len(listed) != 0 {
		t.Errorf("Resources() of a stack never provisioned = %+v, %v, want none", listed, err)
	}
}

func TestStackResourcesOfTheNewestReleaseNameTheStackHoldingItsAppsWorkers(t *testing.T) {
	t.Parallel()

	recorded := fake.NewKeyValues()
	stacks, _, spec := inventoriedStacksIn(t, unlistableStore{recorded}, nil)
	older := spec.Ref
	older.Name = naming.AppStack("prod", "web", naming.NewReleaseToken("build-1", "older"))
	newer := spec.Ref
	newer.Name = naming.AppStack("prod", "web", naming.NewReleaseToken("build-2", "newer"))
	recordWorkersStack(t, recorded, workersRef(newer))
	worker := pulumi.ExportedResource{Type: "aws:lambda/function:Function", ID: "ocel-app-shop-prod-web-media"}
	stacks.engine.(*mockedEngine).exported = map[string][]pulumi.ExportedResource{workersRef(newer).Name.String(): {worker}}
	ctx := context.Background()

	ofNewer, err := stacks.Resources(ctx, provider.StackResources{Ref: newer, NewestRelease: true})
	if err != nil {
		t.Fatalf("Resources(newer) = %v, want the workers named without listing every stack of the project", err)
	}
	ofOlder, err := stacks.Resources(ctx, provider.StackResources{Ref: older})
	if err != nil {
		t.Fatalf("Resources(older) = %v", err)
	}

	held := provider.PhysicalResource{Kind: "AWS::Lambda::Function", ID: worker.ID, Location: "eu-west-1"}
	if !slices.Contains(ofNewer, held) {
		t.Errorf("Resources(newer) = %+v, want the workers stack's function: the newest release names it", ofNewer)
	}
	if slices.Contains(ofOlder, held) {
		t.Errorf("Resources(older) = %+v, want no workers: they are named once, under the newest release", ofOlder)
	}
}
