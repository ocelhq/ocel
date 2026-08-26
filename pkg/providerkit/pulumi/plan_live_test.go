package pulumi_test

import (
	"context"
	"os"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/providerkit"
	kitpulumi "github.com/ocelhq/ocel/pkg/providerkit/pulumi"
)

type componentProgram struct{ names []string }

func (p componentProgram) Run(ctx *pulumi.Context, _ providerkit.StackPlan) error {
	for _, name := range p.names {
		var component pulumi.ResourceState
		if err := ctx.RegisterComponentResource("ocel:proto630:Thing", name, &component); err != nil {
			return err
		}
	}
	return nil
}

func TestPreviewProducesPlanRows(t *testing.T) {
	if os.Getenv("OCEL_LIVE_PULUMI") == "" {
		t.Skip("set OCEL_LIVE_PULUMI=1 to run a real pulumi preview")
	}
	dir := t.TempDir()
	adapter := kitpulumi.New(kitpulumi.Config{
		Access: kitpulumi.Access{
			BackendURL: "file://" + dir,
			Passphrase: "proto630",
			Project:    "proto630",
		},
		Program: componentProgram{names: []string{"alpha", "beta", "gamma"}},
	})

	ref := providerkit.StackRef{
		Project: "proto630",
		Class:   providerkit.ClassProduction,
		Name:    naming.InfraStack("proto630"),
	}

	group, err := adapter.Plan(context.Background(), providerkit.StackPlan{Ref: ref}, nil)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if len(group.Changes) == 0 {
		t.Fatal("a preview over three component resources produced no plan rows")
	}
	creates := 0
	for _, change := range group.Changes {
		t.Logf("%s %s %s", change.Action, change.Kind, change.Name)
		if change.Action == providerkit.ActionCreate {
			creates++
		}
	}
	if creates < len(group.Changes) {
		t.Fatalf("a first preview should plan only creates, got %d of %d", creates, len(group.Changes))
	}
	if group.Action != providerkit.ActionCreate {
		t.Fatalf("group rolled up to %q, want create", group.Action)
	}
}
