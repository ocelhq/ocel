package account

import (
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/platform/aws/provider/tier/taginvalidator"
)

func TestBothCredentialsMakeAndRemoveTheTagInvalidatorQueueOfOnlyTheirOwnTier(t *testing.T) {
	queueOf := func(tier environment.Tier) string {
		return "arn:aws:sqs:us-east-1:111122223333:" + taginvalidator.NamesFor(string(defaultNamespace), tier).Queue
	}
	creating := func(tier environment.Tier) requestContext {
		return requestContext{"aws:RequestTag/" + naming.ManagedByTagKey: string(defaultNamespace), "aws:RequestTag/" + naming.EnvTierTagKey: string(tier)}
	}
	for _, tier := range bothTiers {
		sibling := tier.Sibling()
		for purpose, document := range credentialsOfTier(t, tier) {
			if !allows(t, document, "sqs:CreateQueue", queueOf(tier), creating(tier)) {
				t.Errorf("the %s %s credential cannot create %s tagged for its tier", tier, purpose, queueOf(tier))
			}
			for _, action := range []string{"sqs:GetQueueUrl", "sqs:SetQueueAttributes", "sqs:DeleteQueue"} {
				if !allows(t, document, action, queueOf(tier), appResourceOf(tier, nil)) {
					t.Errorf("the %s %s credential cannot %s on %s, its own tier's invalidator queue", tier, purpose, action, queueOf(tier))
				}
				if allows(t, document, action, queueOf(sibling), appResourceOf(sibling, nil)) {
					t.Errorf("the %s %s credential can %s on %s, the %s tier's invalidator queue", tier, purpose, action, queueOf(sibling), sibling)
				}
			}
		}
	}
}
