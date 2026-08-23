package ports_test

import (
	"context"
	"reflect"
	"slices"
	"testing"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/providerkit"
	awsports "github.com/ocelhq/ocel/platform/aws/provider/ports"
)

type sweptClock struct {
	swept []string
	err   error
}

func (c *sweptClock) SweepTagClock(_ context.Context, project string, stack naming.StackName) error {
	c.swept = append(c.swept, project+"/"+stack.String())
	return c.err
}

func newStacks(t *testing.T) (awsports.Stacks, *sweptClock) {
	t.Helper()
	records, _ := newRecords(t)
	clock := &sweptClock{}
	return awsports.Stacks{Records: records, Tags: clock}, clock
}

func release(t *testing.T, id string) naming.Release {
	t.Helper()
	return naming.NewRelease(id, "")
}

func TestStacksIndexesAProjectAndItsStacks(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	stacks, _ := newStacks(t)
	web := naming.AppStack("prod", "web", release(t, "rcafef00d"))

	if err := stacks.AddProject(ctx, "shop", []string{"isr"}); err != nil {
		t.Fatalf("AddProject: %v", err)
	}
	if err := stacks.AddStack(ctx, "shop", web); err != nil {
		t.Fatalf("AddStack: %v", err)
	}
	if err := stacks.AddStack(ctx, "shop", naming.InfraStack("prod")); err != nil {
		t.Fatalf("AddStack(infra): %v", err)
	}

	projects, err := stacks.Projects(ctx)
	if err != nil {
		t.Fatalf("Projects: %v", err)
	}
	if !reflect.DeepEqual(projects, []string{"shop"}) {
		t.Errorf("Projects = %v, want the stacks left out of the project listing", projects)
	}

	held, err := stacks.Stacks(ctx, "shop")
	if err != nil {
		t.Fatalf("Stacks: %v", err)
	}
	if !slices.Contains(held, web) || len(held) != 2 {
		t.Errorf("Stacks = %v, want both stacks this project realized", held)
	}
}

func TestStacksRecordTheFeaturesAProjectNeeds(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	records, _ := newRecords(t)
	stacks := awsports.Stacks{Records: records}

	if err := stacks.AddProject(ctx, "shop", []string{"isr", "image-optimizer"}); err != nil {
		t.Fatalf("AddProject: %v", err)
	}

	gate := providerkit.Gate{Records: records}
	recorded, err := gate.RecordedFeatures(ctx)
	if err != nil {
		t.Fatalf("RecordedFeatures: %v", err)
	}
	if !reflect.DeepEqual(recorded["shop"], []string{"isr", "image-optimizer"}) {
		t.Errorf("recorded features = %v, want what the deploy indexed: the gate refuses to drop a feature a project still needs", recorded)
	}
}

func TestStacksSweepsTheTagClockWhenATeardownAsksItTo(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	stacks, clock := newStacks(t)
	web := naming.AppStack("prod", "web", release(t, "rcafef00d"))

	if err := stacks.AddProject(ctx, "shop", nil); err != nil {
		t.Fatalf("AddProject: %v", err)
	}
	if err := stacks.AddStack(ctx, "shop", web); err != nil {
		t.Fatalf("AddStack: %v", err)
	}
	if err := stacks.RemoveStack(ctx, "shop", web); err != nil {
		t.Fatalf("RemoveStack: %v", err)
	}
	if len(clock.swept) != 0 {
		t.Errorf("swept = %v from the index alone, want the teardown to be what asks for the sweep", clock.swept)
	}
	if err := stacks.SweepTagClock(ctx, "shop", web); err != nil {
		t.Fatalf("SweepTagClock: %v", err)
	}

	if !reflect.DeepEqual(clock.swept, []string{"shop/" + web.String()}) {
		t.Errorf("swept = %v, want the stack's tag rows taken with it", clock.swept)
	}
	held, err := stacks.Stacks(ctx, "shop")
	if err != nil {
		t.Fatalf("Stacks: %v", err)
	}
	if len(held) != 0 {
		t.Errorf("Stacks = %v, want the stack forgotten", held)
	}

	if err := stacks.RemoveStack(ctx, "shop", web); err != nil {
		t.Errorf("repeated RemoveStack = %v, want a re-run of a finished teardown to pass", err)
	}
	if err := stacks.RemoveProject(ctx, "shop"); err != nil {
		t.Fatalf("RemoveProject: %v", err)
	}
	projects, err := stacks.Projects(ctx)
	if err != nil {
		t.Fatalf("Projects: %v", err)
	}
	if len(projects) != 0 {
		t.Errorf("Projects = %v, want the torn-down project gone", projects)
	}
}

func TestStacksRefuseANameNothingCanRead(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	stacks, _ := newStacks(t)

	if err := stacks.AddProject(ctx, "Not A Slug", nil); err == nil {
		t.Error("AddProject with an unreadable project = nil, want it refused before anything is written")
	}
	if err := stacks.AddStack(ctx, "shop", naming.StackName{}); err == nil {
		t.Error("AddStack with an unreadable stack name = nil, want it refused")
	}
}
