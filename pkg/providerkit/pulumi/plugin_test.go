package pulumi_test

import (
	"context"
	"sync/atomic"
	"testing"

	provider "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi-go-provider/infer"
	sdk "github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/providerkit/pulumi"
)

type pushCounter struct{ created atomic.Int64 }

type pushArgs struct {
	Set string `pulumi:"set"`
}

type pushState struct {
	pushArgs
	Pushed bool `pulumi:"pushed"`
}

type pushResource struct{ counter *pushCounter }

func (*pushResource) Annotate(a infer.Annotator) { a.SetToken("index", "Push") }

func (r *pushResource) Create(_ context.Context, req infer.CreateRequest[pushArgs]) (infer.CreateResponse[pushState], error) {
	if req.DryRun {
		return infer.CreateResponse[pushState]{ID: req.Inputs.Set, Output: pushState{pushArgs: req.Inputs}}, nil
	}
	r.counter.created.Add(1)
	return infer.CreateResponse[pushState]{ID: req.Inputs.Set, Output: pushState{pushArgs: req.Inputs, Pushed: true}}, nil
}

type pushing struct{}

func (pushing) Run(pctx *sdk.Context, _ providerkit.StackPlan) error {
	state := &struct{ sdk.CustomResourceState }{}
	return pctx.RegisterResource("probe:index:Push", "assets", sdk.Map{"set": sdk.String("assets")}, state)
}

func probePlugin(t *testing.T, counter *pushCounter) pulumi.Plugin {
	t.Helper()
	built, err := infer.NewProviderBuilder().
		WithNamespace("probe").
		WithResources(infer.Resource(&pushResource{counter: counter})).
		Build()
	if err != nil {
		t.Fatalf("build the probe plugin: %v", err)
	}
	var _ provider.Provider = built
	return pulumi.Plugin{Package: "probe", Version: "1.0.0", Provider: built}
}

func TestAnAttachedPluginPushesOnApplyAndNeverOnPlan(t *testing.T) {
	if testing.Short() {
		t.Skip("this drives the real engine, which installs and runs the pinned Pulumi runtime")
	}

	ctx := context.Background()
	counter := &pushCounter{}
	backend := "file://" + t.TempDir()
	adapter := pulumi.New(pulumi.Config{
		Access: pulumi.Access{
			BackendURL: backend,
			Passphrase: "a-passphrase",
			Project:    "ocel-plugin-probe",
		},
		Program: pushing{},
		Plugins: []pulumi.Plugin{probePlugin(t, counter)},
	})

	ref := providerkit.StackRef{
		Project: "probe",
		Class:   providerkit.ClassPreview,
		Name:    naming.InfraStack("probe"),
	}
	plan := providerkit.StackPlan{Ref: ref, Kind: providerkit.StackInfra}

	planned, err := adapter.Preview(ctx, plan, nil)
	if err != nil {
		t.Fatalf("Preview() over an attached plugin = %v", err)
	}
	if rows := changedRows(planned); rows == 0 {
		t.Fatal("Preview() showed no row for the plugin's resource, and the plan is the only thing a human consents to")
	}
	if pushed := counter.created.Load(); pushed != 0 {
		t.Fatalf("Preview() pushed %d times, want none: a plan writes nothing", pushed)
	}

	if _, err := adapter.Run(ctx, plan, nil); err != nil {
		t.Fatalf("Run() over an attached plugin = %v", err)
	}
	if pushed := counter.created.Load(); pushed != 1 {
		t.Fatalf("Run() pushed %d times, want the one the plan showed", pushed)
	}

	if err := adapter.Destroy(ctx, ref, nil); err != nil {
		t.Fatalf("Destroy() of the stack the plugin wrote into = %v", err)
	}
}

func changedRows(plan providerkit.Plan) int {
	rows := 0
	for _, group := range plan.Groups {
		rows += len(group.Changes)
	}
	return rows
}
