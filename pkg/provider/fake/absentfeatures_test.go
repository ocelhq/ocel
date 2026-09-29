package fake_test

import (
	"context"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
)

func TestAFeatureDescribedAbsentIsListedAsAStackThatIsNotPresentUntilItIsApplied(t *testing.T) {
	t.Parallel()

	b := fake.NewBootstrap()
	b.DescribeAbsent(fake.FeatureImages)
	if err := b.Apply(context.Background(), provider.BootstrapRequest{Tier: environment.TierProduction}, nil); err != nil {
		t.Fatal(err)
	}
	if present, listed := stackOf(t, b, fake.FeatureImages); !listed || present {
		t.Errorf("the %s stack is listed %v and present %v, want it listed as not present", fake.FeatureImages, listed, present)
	}

	if err := b.Apply(context.Background(), provider.BootstrapRequest{Tier: environment.TierProduction, Features: []string{fake.FeatureImages}}, nil); err != nil {
		t.Fatal(err)
	}
	if present, _ := stackOf(t, b, fake.FeatureImages); !present {
		t.Errorf("the %s stack is not present once applied", fake.FeatureImages)
	}
}

func stackOf(t *testing.T, b *fake.Bootstrap, feature string) (present, listed bool) {
	t.Helper()
	described, err := b.Describe(context.Background(), environment.TierProduction)
	if err != nil {
		t.Fatal(err)
	}
	for _, stack := range described.Stacks {
		if stack.Feature == feature {
			return stack.Present, true
		}
	}
	return false, false
}
