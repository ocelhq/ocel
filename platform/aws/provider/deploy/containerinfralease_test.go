package deploy

import (
	"context"
	"errors"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/auto"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/refusal"
)

type interceptedRecords struct {
	keyvalue.Store
	afterList func(under keyvalue.Key)
}

func (r *interceptedRecords) List(ctx context.Context, in keyvalue.Partition, under ...string) ([]keyvalue.Entry, error) {
	listed, err := r.Store.List(ctx, in, under...)
	if r.afterList != nil {
		r.afterList(in.Key(under...))
	}
	return listed, err
}

func listConsumers(ctx context.Context, store keyvalue.Store) ([]keyvalue.Entry, error) {
	consumers := consumersPrefix(environment.TierProduction)
	return store.List(ctx, consumers.Partition, consumers.Path...)
}

func containerStacks(t *testing.T, store keyvalue.Store) (*Stacks, *mockedEngine, provider.StackSpec) {
	t.Helper()
	cfg, spec := containerStackSpec(t)
	cfg.KeyValues = store
	cfg.BackendURL = "s3://ocel-state/conformance"
	cfg.PulumiProject = "ocel-conformance"
	cfg.Passphrase = "a-passphrase"
	outputs := containerInfraOutputs()
	outputs["web"] = auto.OutputValue{Value: map[string]any{
		outputKeyContainerURL:      "http://" + fixtureOrigin,
		outputKeyContainerPhysical: containerPhysical,
	}}
	engine := &mockedEngine{outputs: outputs}
	return stacksWith(cfg, engine), engine, spec
}

func TestTheLastContainerLeavingKeepsTheContainerInfraWhenAnotherDeployClaimsItMeanwhile(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	shared := fake.NewKeyValues()
	store := &interceptedRecords{Store: shared}
	stacks, engine, shop := containerStacks(t, store)
	if _, err := stacks.Provision(ctx, shop, progress.Discard()); err != nil {
		t.Fatalf("Provision(shop) = %v", err)
	}

	other, _, blog := containerStacks(t, shared)
	blog.Ref = provider.StackRef{Project: "blog", Tier: environment.TierProduction, Name: naming.AppStack("prod", "web", fixedRelease(t))}
	claimed := false
	store.afterList = func(under keyvalue.Key) {
		if claimed || under.String() != consumersPrefix(environment.TierProduction).String() {
			return
		}
		claimed = true
		if _, err := other.Provision(ctx, blog, progress.Discard()); err != nil {
			t.Errorf("Provision(blog) during shop's release = %v", err)
		}
	}

	if err := stacks.Destroy(ctx, shop.Ref, progress.Discard()); err != nil {
		t.Fatalf("Destroy(shop) = %v", err)
	}
	if !claimed {
		t.Fatal("the concurrent claim never ran, so this test proved nothing")
	}
	for _, torn := range engine.torn() {
		if torn == containerInfraRef(environment.TierProduction).Name.String() {
			t.Fatal("the shared container infrastructure was torn down under blog, which claimed it between shop's listing and its lease")
		}
	}
	remaining, err := listConsumers(ctx, shared)
	if err != nil {
		t.Fatal(err)
	}
	if len(remaining) != 1 {
		t.Errorf("%d consumers remain, want blog alone", len(remaining))
	}
}

func TestAContainerDeployIsRefusedWhileTheContainerInfraIsGoingDown(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	shared := fake.NewKeyValues()
	stacks, engine, shop := containerStacks(t, shared)
	if _, err := stacks.Provision(ctx, shop, progress.Discard()); err != nil {
		t.Fatalf("Provision(shop) = %v", err)
	}
	leased, err := keyvalue.ReadOrEmpty(ctx, shared, leaseAt(environment.TierProduction))
	if err != nil {
		t.Fatal(err)
	}
	if err := writeLease(ctx, shared, leased, lease{Destroying: true}); err != nil {
		t.Fatal(err)
	}

	other, _, blog := containerStacks(t, shared)
	blog.Ref = provider.StackRef{Project: "blog", Tier: environment.TierProduction, Name: naming.AppStack("prod", "web", fixedRelease(t))}
	_, err = other.Provision(ctx, blog, progress.Discard())
	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeBusy {
		t.Fatalf("Provision(blog) = %v, want the busy refusal shared container infrastructure on its way down earns", err)
	}
	remaining, err := listConsumers(ctx, shared)
	if err != nil {
		t.Fatal(err)
	}
	if len(remaining) != 1 {
		t.Errorf("%d consumers remain, want shop alone: a refused claim must not leave a record that would keep the shared container infrastructure up", len(remaining))
	}
	if ran := engine.stacks(); len(ran) != 2 {
		t.Errorf("the engine ran %v, want nothing more than shop's container infrastructure stack and app stack", ran)
	}
}
