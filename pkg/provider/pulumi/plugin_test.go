package pulumi_test

import (
	"context"
	"path/filepath"
	"slices"
	"sync/atomic"
	"testing"

	"github.com/pulumi/pulumi-go-provider/infer"
	sdk "github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/pulumi"
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

func (pushing) Run(pctx *sdk.Context, _ provider.StackSpec) error {
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
	var _ = built
	return pulumi.Plugin{Package: "probe", Version: "1.0.0", Provider: built}
}

func TestAnAttachedPluginPushesOnApplyAndNeverOnPlan(t *testing.T) {
	if testing.Short() {
		t.Skip("this drives the real engine, which installs and runs the pinned Pulumi runtime")
	}

	ctx := context.Background()
	counter := &pushCounter{}
	backend := "file://" + t.TempDir()
	automation := pulumi.New(pulumi.Config{
		Backend: pulumi.Backend{
			URL:        backend,
			Passphrase: "a-passphrase",
			Project:    "ocel-plugin-probe",
		},
		Program: pushing{}.Run,
		Plugins: []pulumi.Plugin{probePlugin(t, counter)},
	})

	ref := provider.StackRef{
		Project: "probe",
		Tier:    environment.TierPreview,
		Name:    naming.InfraStack("probe"),
	}
	spec := provider.StackSpec{Ref: ref, Kind: provider.StackInfra}

	planned, err := automation.Preview(ctx, spec, nil)
	if err != nil {
		t.Fatalf("Preview() over an attached plugin = %v", err)
	}
	if rows := changedRows(planned); rows == 0 {
		t.Fatal("Preview() showed no row for the plugin's resource, and the plan is the only thing a human consents to")
	}
	if pushed := counter.created.Load(); pushed != 0 {
		t.Fatalf("Preview() pushed %d times, want none: a plan writes nothing", pushed)
	}

	if _, err := automation.Run(ctx, spec, nil); err != nil {
		t.Fatalf("Run() over an attached plugin = %v", err)
	}
	if pushed := counter.created.Load(); pushed != 1 {
		t.Fatalf("Run() pushed %d times, want the one the plan showed", pushed)
	}

	if err := automation.Destroy(ctx, ref, nil); err != nil {
		t.Fatalf("Destroy() of the stack the plugin wrote into = %v", err)
	}
}

func TestPreviewDestroyOfAProvisionedStackShowsEachResourceGoingOnThePinnedRuntime(t *testing.T) {
	if testing.Short() {
		t.Skip("this drives the real engine, which installs and runs the pinned Pulumi runtime")
	}

	ctx := context.Background()
	automation := pulumi.New(pulumi.Config{
		Backend: pulumi.Backend{URL: "file://" + t.TempDir(), Passphrase: "a-passphrase", Project: "ocel-plugin-probe"},
		Program: pushing{}.Run,
		Plugins: []pulumi.Plugin{probePlugin(t, &pushCounter{})},
	})
	ref := provider.StackRef{Project: "probe", Tier: environment.TierProduction, Name: naming.InfraStack("prod")}
	if _, err := automation.Run(ctx, provider.StackSpec{Ref: ref, Kind: provider.StackInfra}, nil); err != nil {
		t.Fatalf("Run() = %v", err)
	}

	planned, err := automation.PreviewDestroy(ctx, ref, nil)
	if err != nil {
		t.Fatalf("PreviewDestroy() on Pulumi %s = %v", pulumi.PinnedVersion, err)
	}
	if len(planned.Groups) != 1 || planned.Groups[0].Action != provider.ActionDelete {
		t.Fatalf("PreviewDestroy() = %+v, want the stack shown as going", planned.Groups)
	}
	going := planned.Groups[0].Changes
	want := provider.Change{Kind: "probe:index:Push", Name: "assets", Action: provider.ActionDelete}
	if !slices.Contains(going, want) {
		t.Errorf("PreviewDestroy() shows %+v, want %+v among them: a teardown is consented to by what it shows", going, want)
	}
	for _, change := range going {
		if change.Action != provider.ActionDelete {
			t.Errorf("PreviewDestroy() shows %s as %q, and a teardown takes everything down", change.Name, change.Action)
		}
	}

	if err := automation.Destroy(ctx, ref, nil); err != nil {
		t.Fatalf("Destroy() = %v", err)
	}
}

func TestPreviewDestroyOfAStackNeverProvisionedShowsNothingAndWritesNoStack(t *testing.T) {
	if testing.Short() {
		t.Skip("this drives the real engine, which installs and runs the pinned Pulumi runtime")
	}

	dir := t.TempDir()
	automation := pulumi.New(pulumi.Config{
		Backend: pulumi.Backend{URL: "file://" + dir, Passphrase: "a-passphrase", Project: "ocel-plugin-probe"},
		Program: pushing{}.Run,
		Plugins: []pulumi.Plugin{probePlugin(t, &pushCounter{})},
	})
	ref := provider.StackRef{Project: "probe", Tier: environment.TierProduction, Name: naming.InfraStack("prod")}

	planned, err := automation.PreviewDestroy(context.Background(), ref, nil)
	if err != nil {
		t.Fatalf("PreviewDestroy() = %v", err)
	}
	if len(planned.Groups) != 0 {
		t.Errorf("PreviewDestroy() = %+v, want nothing: a stack never provisioned has nothing to take down", planned.Groups)
	}
	if written, _ := filepath.Glob(filepath.Join(dir, ".pulumi", "stacks", "*", "*")); len(written) != 0 {
		t.Errorf("planning a teardown wrote %v, want no stack: a plan writes nothing", written)
	}
}

func changedRows(plan provider.Plan) int {
	rows := 0
	for _, group := range plan.Groups {
		rows += len(group.Changes)
	}
	return rows
}
