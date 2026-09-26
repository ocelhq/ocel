package deploy

import (
	"context"
	"errors"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/auto"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/providerkit/fake"
	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	"github.com/ocelhq/ocel/pkg/providerkit/records"
	"github.com/ocelhq/ocel/pkg/providerkit/refusal"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type interceptedRecords struct {
	records.Store
	afterList func(under records.Name)
}

func (r *interceptedRecords) List(ctx context.Context, under records.Name) ([]records.Record, error) {
	held, err := r.Store.List(ctx, under)
	if r.afterList != nil {
		r.afterList(under)
	}
	return held, err
}

func containerStacks(t *testing.T, records records.Store) (*Stacks, *mockedEngine, provider.StackSpec) {
	t.Helper()
	cfg, spec := containerStackSpec(t)
	cfg.Records = records
	cfg.BackendURL = "s3://ocel-state/conformance"
	cfg.PulumiProject = "ocel-conformance"
	cfg.Passphrase = "a-passphrase"
	outputs := substrateOutputs()
	outputs["web"] = auto.OutputValue{Value: map[string]any{
		outputKeyContainerURL:      "http://" + fixtureOrigin,
		outputKeyContainerPhysical: containerPhysical,
	}}
	engine := &mockedEngine{outputs: outputs}
	return standingUp(cfg, engine), engine, spec
}

func TestTheLastContainerLeavingKeepsTheSubstrateWhenAnotherDeployClaimsItMeanwhile(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	shared := fake.NewRecords()
	store := &interceptedRecords{Store: shared}
	stacks, engine, shop := containerStacks(t, store)
	if _, err := stacks.Provision(ctx, shop, edge.DiscardProgress()); err != nil {
		t.Fatalf("Provision(shop) = %v", err)
	}

	other, _, blog := containerStacks(t, shared)
	blog.Ref = provider.StackRef{Project: "blog", Class: edge.ClassProduction, Name: naming.AppStack("prod", "web", fixedRelease(t))}
	claimed := false
	store.afterList = func(under records.Name) {
		if claimed || under.String() != consumersRecord(edge.ClassProduction).String() {
			return
		}
		claimed = true
		if _, err := other.Provision(ctx, blog, edge.DiscardProgress()); err != nil {
			t.Errorf("Provision(blog) during shop's release = %v", err)
		}
	}

	if err := stacks.Destroy(ctx, shop.Ref, edge.DiscardProgress()); err != nil {
		t.Fatalf("Destroy(shop) = %v", err)
	}
	if !claimed {
		t.Fatal("the concurrent claim never ran, so this test proved nothing")
	}
	for _, torn := range engine.torn() {
		if torn == substrateRef(edge.ClassProduction).Name.String() {
			t.Fatal("the substrate was torn down under blog, which claimed it between shop's listing and its lease")
		}
	}
	remaining, err := shared.List(ctx, consumersRecord(edge.ClassProduction))
	if err != nil {
		t.Fatal(err)
	}
	if len(remaining) != 1 {
		t.Errorf("%d consumers remain, want blog alone", len(remaining))
	}
}

func TestAContainerDeployIsRefusedWhileTheSubstrateIsGoingDown(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	shared := fake.NewRecords()
	stacks, engine, shop := containerStacks(t, shared)
	if _, err := stacks.Provision(ctx, shop, edge.DiscardProgress()); err != nil {
		t.Fatalf("Provision(shop) = %v", err)
	}
	held, err := records.ReadOrEmpty(ctx, shared, leaseRecord(edge.ClassProduction))
	if err != nil {
		t.Fatal(err)
	}
	if err := writeLease(ctx, shared, held, lease{Destroying: true}); err != nil {
		t.Fatal(err)
	}

	other, _, blog := containerStacks(t, shared)
	blog.Ref = provider.StackRef{Project: "blog", Class: edge.ClassProduction, Name: naming.AppStack("prod", "web", fixedRelease(t))}
	_, err = other.Provision(ctx, blog, edge.DiscardProgress())
	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeBusy {
		t.Fatalf("Provision(blog) = %v, want the busy refusal a substrate on its way down earns", err)
	}
	remaining, err := shared.List(ctx, consumersRecord(edge.ClassProduction))
	if err != nil {
		t.Fatal(err)
	}
	if len(remaining) != 1 {
		t.Errorf("%d consumers remain, want shop alone: a refused claim must not leave a record that would keep the substrate up", len(remaining))
	}
	if ran := engine.stacks(); len(ran) != 2 {
		t.Errorf("the engine ran %v, want nothing more than shop's substrate and stack", ran)
	}
}
