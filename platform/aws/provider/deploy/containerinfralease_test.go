package deploy

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/pulumi/pulumi/sdk/v3/go/auto"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	"github.com/ocelhq/ocel/platform/aws/provider/tier/containers"
	"github.com/ocelhq/ocel/platform/aws/provider/tier/sharedstack"
)

type interceptedRecords struct {
	records.Store
	afterList func(under records.Key)
}

func (r *interceptedRecords) List(ctx context.Context, in records.Partition, under ...string) ([]records.Entry, error) {
	listed, err := r.Store.List(ctx, in, under...)
	if r.afterList != nil {
		r.afterList(in.Key(under...))
	}
	return listed, err
}

func listConsumers(ctx context.Context, store records.Store) ([]records.Entry, error) {
	consumers := containers.Shared.ConsumersPrefix(environment.TierProduction)
	return store.List(ctx, consumers.Partition, consumers.Path...)
}

func containerStacks(t *testing.T, store records.Store) (*Stacks, *mockedEngine, provider.StackSpec) {
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
	store.afterList = func(under records.Key) {
		if claimed || under.String() != containers.Shared.ConsumersPrefix(environment.TierProduction).String() {
			return
		}
		claimed = true
		if _, err := other.Provision(ctx, blog, progress.Discard()); err != nil {
			t.Errorf("Provision(blog) during shop's release = %v", err)
		}
	}

	if err := stacks.Destroy(ctx, provider.StackDestroy{Ref: shop.Ref}, progress.Discard()); err != nil {
		t.Fatalf("Destroy(shop) = %v", err)
	}
	if !claimed {
		t.Fatal("the concurrent claim never ran, so this test proved nothing")
	}
	for _, torn := range engine.torn() {
		if torn == containers.StackRef(environment.TierProduction).Name.String() {
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
	leased, err := records.ReadOrEmpty(ctx, shared, containers.Shared.LeaseAt(environment.TierProduction))
	if err != nil {
		t.Fatal(err)
	}
	if err := containers.Shared.WriteLease(ctx, shared, leased, sharedstack.Lease{Destroying: true, DestroyingSince: time.Now().UTC()}); err != nil {
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

func failingOnce(stack string) func(string) error {
	failed := false
	return func(torn string) error {
		if torn != stack || failed {
			return nil
		}
		failed = true
		return errors.New("DependencyViolation: the resource has a dependent object")
	}
}

func TestAContainerDeployAfterAFailedTakedownRaisesTheContainerInfraAgain(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	shared := fake.NewKeyValues()
	stacks, engine, shop := containerStacks(t, shared)
	if _, err := stacks.Provision(ctx, shop, progress.Discard()); err != nil {
		t.Fatalf("Provision(shop) = %v", err)
	}
	infra := containers.StackRef(environment.TierProduction).Name.String()
	engine.downErr = failingOnce(infra)
	if err := stacks.Destroy(ctx, provider.StackDestroy{Ref: shop.Ref}, progress.Discard()); err == nil {
		t.Fatal("Destroy(shop) = nil, want the failed takedown reported")
	}

	blog := shop
	blog.Ref = provider.StackRef{Project: "blog", Tier: environment.TierProduction, Name: naming.AppStack("prod", "web", fixedRelease(t))}
	if _, err := stacks.Provision(ctx, blog, progress.Discard()); err != nil {
		t.Fatalf("Provision(blog) after the failed takedown = %v, want it to proceed rather than wait on a takedown nobody is running", err)
	}

	ran := engine.stacks()
	if len(ran) < 2 || ran[len(ran)-2] != infra || ran[len(ran)-1] != blog.Ref.Name.String() {
		t.Errorf("ran %v, want the container infrastructure run again before blog: the failed takedown may have removed part of it", ran)
	}
}

func TestAFailedTakedownLeavesTheContainersPartForItsRemovalToTakeDown(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	shared := fake.NewKeyValues()
	stacks, engine, shop := containerStacks(t, shared)
	if _, err := stacks.Provision(ctx, shop, progress.Discard()); err != nil {
		t.Fatalf("Provision(shop) = %v", err)
	}
	infra := containers.StackRef(environment.TierProduction).Name.String()
	engine.downErr = failingOnce(infra)

	err := stacks.Destroy(ctx, provider.StackDestroy{Ref: shop.Ref}, progress.Discard())
	if err == nil || !strings.Contains(err.Error(), "ocel bootstrap production --remove containers") {
		t.Fatalf("Destroy(shop) = %v, want the failed takedown to name the command that takes it down again", err)
	}
	part, present, err := containers.ReadPart(ctx, shared, "us-east-1", environment.TierProduction)
	if err != nil || !present || len(part.UsedBy) != 0 {
		t.Fatalf("containers part = %+v, %v, %v, want it still recorded and used by nobody", part, present, err)
	}

	if err := stacks.containers.Remove(ctx, environment.TierProduction, progress.Discard()); err != nil {
		t.Fatalf("containers.Remove() after the failed takedown = %v", err)
	}
	if torn := engine.torn(); !slices.Equal(torn, []string{shop.Ref.Name.String(), infra}) {
		t.Errorf("torn down = %v, want the removal to take the container infrastructure down", torn)
	}
	if _, present, err := stackrecords.ReadBootstrapPart(ctx, shared, environment.TierProduction, containers.Part); err != nil || present {
		t.Errorf("containers record present %v (%v) after its removal, want it forgotten", present, err)
	}
}

func TestAContainerDeployTakesOverATakedownLeaseACrashedRunLeftBehind(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	shared := fake.NewKeyValues()
	stacks, engine, shop := containerStacks(t, shared)
	if _, err := stacks.Provision(ctx, shop, progress.Discard()); err != nil {
		t.Fatalf("Provision(shop) = %v", err)
	}
	leased, err := records.ReadOrEmpty(ctx, shared, containers.Shared.LeaseAt(environment.TierProduction))
	if err != nil {
		t.Fatal(err)
	}
	crashed := sharedstack.Lease{Destroying: true, DestroyingSince: time.Now().UTC().Add(-2 * sharedstack.TakedownLimit)}
	if err := containers.Shared.WriteLease(ctx, shared, leased, crashed); err != nil {
		t.Fatal(err)
	}

	if _, err := stacks.Provision(ctx, shop, progress.Discard()); err != nil {
		t.Fatalf("Provision(shop) under a takedown lease no run has held for %s = %v, want it taken over", 2*sharedstack.TakedownLimit, err)
	}
	infra := containers.StackRef(environment.TierProduction).Name.String()
	if ran := engine.stacks(); len(ran) != 4 || ran[2] != infra {
		t.Errorf("ran %v, want the container infrastructure run again: the crashed takedown may have removed part of it", ran)
	}
}
