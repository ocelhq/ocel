package bootstrap

import (
	"context"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
)

func TestABroughtKeyIsFencedIntoTheCoreBoundaryOnlyWhileItsFeatureIsRequested(t *testing.T) {
	ctx := context.Background()
	stacks := newFakeCFN()
	frontedBy(t, &fakeEdge{kind: "cloudflare"})
	apis := apisOf(stacks, newFakeSSM(), &fakeIAM{}, preloadedStore())

	if err := Run(ctx, apis, defaultNamespace, environment.TierProduction, Request{VariablesKey: broughtKeyARN}, nil); err != nil {
		t.Fatalf("Run without the feature: %v", err)
	}
	if got, want := stacks.template(coreStackName), coreStackTemplate(defaultNamespace, environment.TierProduction, ""); got != want {
		t.Error("a run that never asked for the variables-key feature wrote the brought key into the core boundary")
	}

	if err := Run(ctx, apis, defaultNamespace, environment.TierProduction, Request{Features: []string{provider.FeatureVariablesKey}, VariablesKey: broughtKeyARN}, nil); err != nil {
		t.Fatalf("Run with the feature: %v", err)
	}
	if got, want := stacks.template(coreStackName), coreStackTemplate(defaultNamespace, environment.TierProduction, broughtKeyARN); got != want {
		t.Error("adding the variables-key feature over a brought key left the core boundary fenced to an alias the key never has")
	}
	if !strings.Contains(stacks.template(coreStackName), broughtKeyARN) {
		t.Error("the core boundary does not name the brought key")
	}
	read, err := Read(ctx, stacks, defaultNamespace, environment.TierProduction)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	for _, stack := range read.Deployed.Stacks {
		if (stack.Name == coreStackName || stack.Feature == provider.FeatureVariablesKey) && !stack.Current() {
			t.Errorf("%s reads as behind straight after it was written: a read must render the core with the key the variables-key stack records", stack.Name)
		}
	}
}
