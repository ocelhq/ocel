package cloudflare

import (
	"context"
	"fmt"
	"os"
	"strings"

	cf "github.com/cloudflare/cloudflare-go/v4"
	"github.com/cloudflare/cloudflare-go/v4/accounts"
	"github.com/cloudflare/cloudflare-go/v4/shared"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func (p *cloudflare) codeEntitlement(ctx context.Context) (edge.CodeEntitlement, error) {
	accountID := os.Getenv(envAccountID)
	if accountID == "" {
		return edge.CodeEntitlement{}, fmt.Errorf("%s is not set", envAccountID)
	}
	plan, granted := p.workersPlan(ctx, accountID)
	return edge.CodeEntitlement{Plan: plan, Granted: granted}, nil
}

const workersPaidPlan = "Workers Paid"

const workersFreePlan = "Workers Free"

func (p *cloudflare) workersPlan(ctx context.Context, accountID string) (string, edge.Entitlement) {
	page, err := p.client.Accounts.Subscriptions.Get(ctx, accounts.SubscriptionGetParams{AccountID: cf.F(accountID)})
	if err != nil {
		fmt.Fprintf(os.Stderr,
			"ocel cloudflare edge: could not read the subscriptions of account %s: %v\n"+
				"%s must have the \"Billing Read\" permission (Account scope) to tell whether the plan runs code at the edge. "+
				"Without it this deploy proceeds, and an account on the Workers Free plan is rejected by Cloudflare when the "+
				"worker is uploaded, after the deploy has begun changing your infrastructure\n",
			accountID, err, envAPIToken)
		return "", edge.EntitlementUnknown
	}
	for _, sub := range page.Result {
		if runsWorkerCode(sub.RatePlan) {
			name := sub.RatePlan.PublicName
			if name == "" {
				name = workersPaidPlan
			}
			return name, edge.EntitlementGranted
		}
	}
	return workersFreePlan, edge.EntitlementWithheld
}

func runsWorkerCode(plan shared.RatePlan) bool {
	if plan.IsContract || plan.ID == shared.RatePlanIDEnterprise || plan.ID == shared.RatePlanIDPartnersEnterprise {
		return true
	}
	id := strings.ToLower(string(plan.ID))
	return strings.Contains(id, "workers") && !strings.Contains(id, "free")
}
