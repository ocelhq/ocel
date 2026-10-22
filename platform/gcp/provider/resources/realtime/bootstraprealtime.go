package realtime

import (
	"context"
	"fmt"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/bootstrap"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/credential"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/policy"
)

const realtimeKeysReaderRole = "roles/secretmanager.secretAccessor"

func Purpose(b bootstrap.Engine, tier environment.Tier) bootstrap.AccountPurpose {
	return bootstrap.AccountPurpose{
		DisplayName: "ocel " + string(tier) + " realtime",
		Description: "the identity the realtime gateways of the " + string(tier) + " tier run as, which may read its gateways' keys and hold the Pub/Sub subscriptions its instances hear their topic through, and nothing else",
		Ungranted:   "it may not hold its gateways' Pub/Sub subscriptions",
		Grant:       grantRealtimeSubscriptions,
		Forget:      forgetRealtimeSubscriptions,
		Granted:     realtimeSubscriptionsGranted,
	}
}

func realtimeMember(c *gcloud.Clients, tier environment.Tier) string {
	return "serviceAccount:" + c.RealtimeAccountEmail(tier)
}

func subscriptionsCondition(c *gcloud.Clients, tier environment.Tier) *policy.Condition {
	return c.DeployCredential(tier).TierCondition()
}

func grantRealtimeSubscriptions(ctx context.Context, b bootstrap.Engine, tier environment.Tier) error {
	return bindRealtimeSubscriptions(ctx, b, tier, true)
}

func forgetRealtimeSubscriptions(ctx context.Context, b bootstrap.Engine, tier environment.Tier) error {
	return bindRealtimeSubscriptions(ctx, b, tier, false)
}

func bindRealtimeSubscriptions(ctx context.Context, b bootstrap.Engine, tier environment.Tier, granting bool) error {
	member := realtimeMember(b.Clients, tier)
	if err := gcloud.BindProjectRole(ctx, b.Clients, member, credential.RealtimeSubscriptionsRole, subscriptionsCondition(b.Clients, tier), granting); err != nil {
		return fmt.Errorf("set whether %s may hold the %s tier's gateway instances' Pub/Sub subscriptions: %w", member, tier, err)
	}
	return nil
}

func realtimeSubscriptionsGranted(ctx context.Context, b bootstrap.Engine, tier environment.Tier) (bool, error) {
	return gcloud.ProjectRoleGranted(ctx, b.Clients, realtimeMember(b.Clients, tier), credential.RealtimeSubscriptionsRole, subscriptionsCondition(b.Clients, tier))
}
