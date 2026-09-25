package deploy

import (
	"context"
	"errors"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/auto"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/providerkit/fake"
	"github.com/ocelhq/ocel/pkg/providerkit/ports"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type interceptedRecords struct {
	providerkit.RecordStore
	afterList func(under providerkit.RecordName)
}

func (r *interceptedRecords) List(ctx context.Context, under providerkit.RecordName) ([]providerkit.Record, error) {
	held, err := r.RecordStore.List(ctx, under)
	if r.afterList != nil {
		r.afterList(under)
	}
	return held, err
}

func containerReleaser(t *testing.T, records providerkit.RecordStore) (*Stacks, *mockedEngine, providerkit.StackPlan) {
	t.Helper()
	cfg, plan := plannedContainerStack(t)
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
	return standingUp(cfg, engine), engine, plan
}

func TestTheLastContainerLeavingKeepsTheSubstrateWhenAnotherDeployClaimsItMeanwhile(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	shared := fake.NewRecords()
	records := &interceptedRecords{RecordStore: shared}
	releaser, engine, shop := containerReleaser(t, records)
	if _, err := releaser.Provision(ctx, shop, edge.DiscardProgress()); err != nil {
		t.Fatalf("Provision(shop) = %v", err)
	}

	other, _, blog := containerReleaser(t, shared)
	blog.Ref = providerkit.StackRef{Project: "blog", Class: providerkit.ClassProduction, Name: naming.AppStack("prod", "web", fixedRelease(t))}
	claimed := false
	records.afterList = func(under providerkit.RecordName) {
		if claimed || under.String() != consumersRecord(providerkit.ClassProduction).String() {
			return
		}
		claimed = true
		if _, err := other.Provision(ctx, blog, edge.DiscardProgress()); err != nil {
			t.Errorf("Provision(blog) during shop's release = %v", err)
		}
	}

	if err := releaser.Destroy(ctx, shop.Ref, edge.DiscardProgress()); err != nil {
		t.Fatalf("Destroy(shop) = %v", err)
	}
	if !claimed {
		t.Fatal("the concurrent claim never ran, so this test proved nothing")
	}
	for _, torn := range engine.torn() {
		if torn == substrateRef(providerkit.ClassProduction).Name.String() {
			t.Fatal("the substrate was torn down under blog, which claimed it between shop's listing and its lease")
		}
	}
	remaining, err := shared.List(ctx, consumersRecord(providerkit.ClassProduction))
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
	releaser, engine, shop := containerReleaser(t, shared)
	if _, err := releaser.Provision(ctx, shop, edge.DiscardProgress()); err != nil {
		t.Fatalf("Provision(shop) = %v", err)
	}
	held, err := ports.Held(ctx, shared, leaseRecord(providerkit.ClassProduction))
	if err != nil {
		t.Fatal(err)
	}
	if err := writeLease(ctx, shared, held, lease{Destroying: true}); err != nil {
		t.Fatal(err)
	}

	other, _, blog := containerReleaser(t, shared)
	blog.Ref = providerkit.StackRef{Project: "blog", Class: providerkit.ClassProduction, Name: naming.AppStack("prod", "web", fixedRelease(t))}
	_, err = other.Provision(ctx, blog, edge.DiscardProgress())
	var refusal providerkit.Refusal
	if !errors.As(err, &refusal) || refusal.Code != providerkit.CodeBusy {
		t.Fatalf("Provision(blog) = %v, want the busy refusal a substrate on its way down earns", err)
	}
	remaining, err := shared.List(ctx, consumersRecord(providerkit.ClassProduction))
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
