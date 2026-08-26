package provider_test

// TODO(spike): throwaway — verifies `ocel bootstrap` logic runs against floci
// via AWS_ENDPOINT_URL alone. Delete once the real live harness lands.

import (
	"context"
	"os"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit"
	provider "github.com/ocelhq/ocel/platform/aws/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/edges"
)

func TestFlociSpikeBootstrapApplyAndReplan(t *testing.T) {
	if os.Getenv("AWS_ENDPOINT_URL") == "" {
		t.Skip("no AWS_ENDPOINT_URL in the environment; run under `eval $(floci env)`")
	}

	ctx := context.Background()
	p, err := provider.New(ctx, providerkit.Options{"region": "us-east-1"})
	if err != nil {
		t.Fatalf("New() = %v", err)
	}

	bootstrapper, err := p.Bootstrap(edges.DefaultKind)
	if err != nil {
		t.Fatalf("Bootstrap() = %v", err)
	}

	class := providerkit.ClassProduction
	fresh, err := bootstrapper.Describe(ctx, class)
	if err != nil {
		t.Fatalf("Describe() against a fresh emulator = %v", err)
	}
	if fresh.Present {
		t.Fatalf("Describe() claims a bootstrap on an emulator nothing has written to: %+v", fresh)
	}

	req := providerkit.BootstrapRequest{Class: class, Writer: "floci-spike", Held: fresh.Held}
	plan, err := bootstrapper.Plan(ctx, req)
	if err != nil {
		t.Fatalf("Plan() = %v", err)
	}
	for _, group := range plan.Groups {
		t.Logf("plan group %q action=%q changes=%d", group.Name, group.Action, len(group.Changes))
		for _, change := range group.Changes {
			t.Logf("  %s %q %s", change.Action, change.Name, change.Reason)
		}
	}

	if err := bootstrapper.Apply(ctx, req, nil); err != nil {
		t.Fatalf("Apply() = %v", err)
	}

	standing, err := bootstrapper.Describe(ctx, class)
	if err != nil {
		t.Fatalf("Describe() after Apply() = %v", err)
	}
	if !standing.Present {
		t.Fatalf("Describe() after Apply() shows no bootstrap: %+v", standing)
	}
	for _, stack := range standing.Stacks {
		t.Logf("stack %q feature=%q present=%v digestCurrent=%v writer=%q",
			stack.Name, stack.Feature, stack.Present, stack.DigestCurrent, stack.Writer)
	}

	again, err := bootstrapper.Plan(ctx, providerkit.BootstrapRequest{Class: class, Writer: "floci-spike", Held: standing.Held})
	if err != nil {
		t.Fatalf("a second Plan() = %v", err)
	}
	for _, group := range again.Groups {
		if group.Action != providerkit.ActionKeep {
			t.Errorf("a second Plan() over a bootstrapped emulator plans group %q as %q, want %q",
				group.Name, group.Action, providerkit.ActionKeep)
		}
	}
}
