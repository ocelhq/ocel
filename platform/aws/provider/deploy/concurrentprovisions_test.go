package deploy

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
)

func TestASecondProvisionOfAStackTheEngineHoldsLockedIsRefusedAsBusy(t *testing.T) {
	t.Parallel()

	entered, release := make(chan struct{}), make(chan struct{})
	var first sync.Once
	state := newEngineState()
	state.holding = func(string) {
		first.Do(func() {
			close(entered)
			<-release
		})
	}
	stacks := conformingStacks(&mockedEngine{outputs: provisionedOutputs(), state: state})
	spec := provider.StackSpec{
		Ref:       provider.StackRef{Project: "conformance", Tier: environment.TierPreview, Name: naming.InfraStack("raced")},
		Kind:      provider.StackInfra,
		Recorded:  true,
		Resources: []provider.Resource{{Name: "c-bucket", Type: provider.BindingBucket, Bucket: &provider.BucketSpec{}}},
	}

	ctx := context.Background()
	held := make(chan error, 1)
	go func() {
		_, err := stacks.Provision(ctx, spec, nil)
		held <- err
	}()
	select {
	case <-entered:
	case err := <-held:
		t.Fatalf("the first Provision() = %v before its engine run took the stack's lock", err)
	case <-time.After(10 * time.Second):
		t.Fatal("the first Provision() never reached its engine run")
	}

	_, err := stacks.Provision(ctx, spec, nil)
	close(release)
	if err := <-held; err != nil {
		t.Fatalf("the first Provision() = %v, want it to finish the stack it holds", err)
	}
	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeBusy {
		t.Errorf("a second Provision() of a stack another run holds locked = %v, want a %s refusal naming the lock", err, refusal.CodeBusy)
	}
}
