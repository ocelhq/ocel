package conformance

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

const (
	heldProvisionTimeout = 10 * time.Second
	busyRefusalTimeout   = 5 * time.Second
)

type heldImages struct {
	entered chan struct{}
	release chan struct{}
	first   sync.Once
}

func newHeldImages() *heldImages {
	return &heldImages{entered: make(chan struct{}), release: make(chan struct{})}
}

func (h *heldImages) Destination() string { return "the held store" }

func (h *heldImages) Has(context.Context, provider.ImagePush) (bool, error) { return false, nil }

func (h *heldImages) ProbePush(context.Context, string) error { return nil }

func (h *heldImages) Remove(context.Context, string) error { return nil }

func (h *heldImages) Push(ctx context.Context, _ provider.ImagePush, _ progress.Log) error {
	holding := false
	h.first.Do(func() {
		holding = true
		close(h.entered)
	})
	if !holding {
		return nil
	}
	select {
	case <-h.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type provisioned struct {
	result provider.StackResult
	err    error
}

func provisionInto(ctx context.Context, stacks provider.Stacks, spec provider.StackSpec) <-chan provisioned {
	done := make(chan provisioned, 1)
	go func() {
		result, err := stacks.Provision(ctx, spec, nil)
		done <- provisioned{result: result, err: err}
	}()
	return done
}

func awaitProvision(t *testing.T, done <-chan provisioned, which string) provisioned {
	t.Helper()

	select {
	case outcome := <-done:
		return outcome
	case <-time.After(heldProvisionTimeout):
		t.Fatalf("the %s Provision() was still running %s after the image push holding it was released", which, heldProvisionTimeout)
		return provisioned{}
	}
}

func runConcurrentProvisions(t *testing.T, facts provider.Facts, stacks provider.Stacks, store keyvalue.Store) {
	t.Helper()

	resources := declared(facts.Bindings)
	if len(resources) == 0 {
		t.Skip("this provider serves no resource primitive, so a release asks for nothing")
	}
	ctx := context.Background()
	images := newHeldImages()
	ref := provider.StackRef{Project: "conformance", Tier: environment.TierPreview, Name: naming.InfraStack("raced")}
	spec := provider.StackSpec{
		Ref: ref, Kind: provider.StackInfra, Recorded: true, Resources: resources[:1],
		Images: provider.ImagePushes{Store: images, Pushes: []provider.ImagePush{conformanceImage()}},
	}
	if _, err := stacks.Plan(ctx, spec, nil); err != nil {
		requireInvalid(t, err, "Plan")
		t.Skip("this provider takes no image push on an infra stack, so this suite has no point inside a provision to hold it at; its own tier holds its engine there")
	}
	defer func() {
		if err := stacks.Destroy(ctx, provider.StackDestroy{Ref: ref}, nil); err != nil {
			t.Errorf("Destroy() of the raced stack = %v", err)
		}
		if store != nil {
			if err := stackrecords.Forget(ctx, store, ref.Tier, ref.Project, ref.Name); err != nil {
				t.Errorf("forgetting the raced stack = %v", err)
			}
		}
	}()

	first := provisionInto(ctx, stacks, spec)
	select {
	case <-images.entered:
	case outcome := <-first:
		t.Fatalf("Provision() returned %v before pushing the image its spec declares, so there is no point inside it to hold", outcome.err)
	case <-time.After(heldProvisionTimeout):
		close(images.release)
		t.Fatalf("Provision() pushed nothing within %s, so there is no point inside it to hold", heldProvisionTimeout)
	}

	second := provisionInto(ctx, stacks, spec)
	var raced provisioned
	waited := false
	select {
	case raced = <-second:
	case <-time.After(busyRefusalTimeout):
		waited = true
	}
	close(images.release)
	held := awaitProvision(t, first, "first")
	if waited {
		raced = awaitProvision(t, second, "second")
		t.Errorf("a second Provision() of %s waited %s for the one inside it rather than refusing, and a deploy that queues behind another applies a plan drawn before that one changed the stack", ref.Name, busyRefusalTimeout)
	}

	if held.err != nil {
		t.Fatalf("the first Provision() = %v, want it to finish the stack it was inside", held.err)
	}
	var refused refusal.Refusal
	switch {
	case raced.err == nil:
		t.Errorf("a second Provision() of %s while another was inside it succeeded too, so two runs wrote one stack at once", ref.Name)
	case !errors.As(raced.err, &refused) || refused.Code != refusal.CodeBusy:
		t.Errorf("a second Provision() of %s while another was inside it = %v, want a %s refusal the CLI renders as a run already working on it", ref.Name, raced.err, refusal.CodeBusy)
	}

	if store != nil {
		if err := stackrecords.Write(ctx, store, ref.Tier, ref.Project, ref.Name, stackrecords.Stack{Kind: provider.StackInfra, Bindings: held.result.Bindings}); err != nil {
			t.Fatalf("recording what the first Provision() returned = %v", err)
		}
	}
	settled := spec
	settled.Images = provider.ImagePushes{}
	planned, err := stacks.Plan(ctx, settled, nil)
	if err != nil {
		t.Fatalf("Plan() of the stack the first Provision() finished = %v", err)
	}
	if writes := listPlannedWrites(planned); len(writes) != 0 {
		t.Errorf("Plan() of the stack the first Provision() finished shows %+v written, so the refused run left it other than the first one did", writes)
	}
}
