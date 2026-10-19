package aws

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/account"
)

func TestAnEnvironmentInATierWithNoBootstrapHasNothingToRetire(t *testing.T) {
	t.Parallel()

	p := NewProvider(Options{}, nil, aws.Config{}, defaultNamespace)
	if _, err := p.deployed.resolve(environment.TierPreview, func() (account.Reading, error) {
		return account.Reading{}, nil
	}); err != nil {
		t.Fatal(err)
	}
	live := provider.LiveApps{Slug: "shop", Tier: environment.TierPreview, Env: "pr-1"}

	plan, err := p.PlanRetirement(context.Background(), live, progress.Discard())
	if err != nil || len(plan.Groups) != 0 {
		t.Errorf("PlanRetirement() = %v, %v, want nothing to retire where the tier holds no bootstrap and so no deploy", plan.Groups, err)
	}
	if err := p.Retire(context.Background(), live, progress.Discard()); err != nil {
		t.Errorf("Retire() = %v, want a pass over a tier no deploy could have reached", err)
	}
}
