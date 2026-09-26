package bootstrap

import (
	"context"
	"strings"
	"testing"
)

func TestABroughtKeyIsFencedIntoTheCoreBoundaryOnlyWhileItsFeatureIsRequested(t *testing.T) {
	ctx := context.Background()
	stacks := newFakeCFN()
	frontedBy(t, &fakeEdge{kind: "cloudflare"})
	apis := apisOf(stacks, newFakeSSM(), &fakeIAM{}, preloadedStore())

	if err := Run(ctx, apis, defaultNamespace, ClassProduction, Request{VarsKey: broughtKeyARN}, nil, nil); err != nil {
		t.Fatalf("Run without the feature: %v", err)
	}
	if got, want := stacks.template(coreStackName), coreStackTemplate(defaultNamespace, ClassProduction, ""); got != want {
		t.Error("a run that never asked for the vars-key feature wrote the brought key into the core boundary")
	}

	if err := Run(ctx, apis, defaultNamespace, ClassProduction, Request{Features: []string{FeatureVarsKey}, VarsKey: broughtKeyARN}, nil, nil); err != nil {
		t.Fatalf("Run with the feature: %v", err)
	}
	if got, want := stacks.template(coreStackName), coreStackTemplate(defaultNamespace, ClassProduction, broughtKeyARN); got != want {
		t.Error("adding the vars-key feature over a brought key left the core boundary fenced to an alias the key never has")
	}
	if !strings.Contains(stacks.template(coreStackName), broughtKeyARN) {
		t.Error("the core boundary does not name the brought key")
	}
	read, err := Read(ctx, stacks, defaultNamespace, ClassProduction)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	for _, stack := range read.Deployed.Stacks {
		if (stack.Name == coreStackName || stack.Feature == FeatureVarsKey) && !stack.Current() {
			t.Errorf("%s reads as behind straight after it was written: a read must render the core with the key the vars-key stack records", stack.Name)
		}
	}
}
