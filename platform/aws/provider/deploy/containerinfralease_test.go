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
	listed, err := r.Store.List(ctx, under)
	if r.afterList != nil {
		r.afterList(under)
	}
	return listed, err
}

func containerStacks(t *testing.T, store records.Store) (*Stacks, *mockedEngine, provider.StackSpec) {
	t.Helper()
	cfg, spec := containerStackSpec(t)
	cfg.Records = store
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
		if torn == containerInfraRef(edge.ClassProduction).Name.String() {
			t.Fatal("the shared container infrastructure was torn down under blog, which claimed it between shop's listing and its lease")
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

func TestAContainerDeployIsRefusedWhileTheContainerInfraIsGoingDown(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	shared := fake.NewRecords()
	stacks, engine, shop := containerStacks(t, shared)
	if _, err := stacks.Provision(ctx, shop, edge.DiscardProgress()); err != nil {
		t.Fatalf("Provision(shop) = %v", err)
	}
	leased, err := records.ReadOrEmpty(ctx, shared, leaseRecord(edge.ClassProduction))
	if err != nil {
		t.Fatal(err)
	}
	if err := writeLease(ctx, shared, leased, lease{Destroying: true}); err != nil {
		t.Fatal(err)
	}

	other, _, blog := containerStacks(t, shared)
	blog.Ref = provider.StackRef{Project: "blog", Class: edge.ClassProduction, Name: naming.AppStack("prod", "web", fixedRelease(t))}
	_, err = other.Provision(ctx, blog, edge.DiscardProgress())
	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeBusy {
		t.Fatalf("Provision(blog) = %v, want the busy refusal shared container infrastructure on its way down earns", err)
	}
	remaining, err := shared.List(ctx, consumersRecord(edge.ClassProduction))
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
