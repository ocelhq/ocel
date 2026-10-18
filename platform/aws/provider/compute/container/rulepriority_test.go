package container

import (
	"context"
	"testing"

	elbv2types "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2/types"
)

const containerPhysical = "shop-prod-web-container-r3f8a1c90"

func TestARuleKeepsThePriorityItAlreadyOwnsOnTheFront(t *testing.T) {
	t.Parallel()

	hashed := rulePriority(containerPhysical, nil)
	cfg, spec := containerStackSpec(t)
	cfg.Rules = &fakeRules{existing: []elbv2types.Rule{ruleRouting(hashed, containerPhysical), ruleRouting(hashed+1, "blog-prod-web-container-r0000aaaa")}}
	work, err := definitionOf(cfg, spec, fixtureContainerInfra())
	if err != nil {
		t.Fatalf("containerWork() = %v", err)
	}
	if err := work.PlaceRule(context.Background(), cfg.Rules); err != nil {
		t.Fatalf("placeRule() = %v", err)
	}
	if work.priority != hashed {
		t.Errorf("priority = %d, want %d: the slot already belongs to this stack's own rule, and a redeploy must not walk away from it", work.priority, hashed)
	}
}
