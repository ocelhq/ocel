package realtime

import (
	"context"
	"fmt"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/bootstrap"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/policy"
)

const (
	realtimeKeysReaderRole = "roles/secretmanager.secretAccessor"
	secretVersionType      = "secretmanager.googleapis.com/SecretVersion"
)

func Purpose(b bootstrap.Engine, tier environment.Tier) bootstrap.AccountPurpose {
	return bootstrap.AccountPurpose{
		DisplayName: "ocel " + string(tier) + " realtime",
		Description: "the identity the realtime gateways of the " + string(tier) + " tier run as, which may read the tier's realtime keys and hold the Pub/Sub subscriptions its instances hear their topic through, and nothing else",
		Ungranted:   "it exists, and it may not read the tier's realtime keys or hold its instances' Pub/Sub subscriptions, so a gateway would never start",
		Grant:       grantRealtimeRoles,
		Forget:      forgetRealtimeRoles,
		Granted:     realtimeRolesGranted,
	}
}

func realtimeMember(c *gcloud.Clients, tier environment.Tier) string {
	return "serviceAccount:" + c.RealtimeAccountEmail(tier)
}

func realtimeKeysCondition(ctx context.Context, b bootstrap.Engine, tier environment.Tier) (*policy.Condition, error) {
	number, err := b.Clients.ReadProjectNumber(ctx)
	if err != nil {
		return nil, err
	}
	return &policy.Condition{
		Title: "ocel " + string(b.Clients.Namespace()) + " " + string(tier) + " realtime keys",
		Expression: fmt.Sprintf("resource.type == %q && resource.name.startsWith(%q)",
			secretVersionType, fmt.Sprintf("projects/%d/secrets/%s", number, b.Clients.RealtimeKeysSecretPrefix(tier))),
	}, nil
}

func grantRealtimeRoles(ctx context.Context, b bootstrap.Engine, tier environment.Tier) error {
	return bindRealtimeRoles(ctx, b, tier, true)
}

func forgetRealtimeRoles(ctx context.Context, b bootstrap.Engine, tier environment.Tier) error {
	return bindRealtimeRoles(ctx, b, tier, false)
}

func bindRealtimeRoles(ctx context.Context, b bootstrap.Engine, tier environment.Tier, granting bool) error {
	condition, err := realtimeKeysCondition(ctx, b, tier)
	if err != nil {
		return err
	}
	member := realtimeMember(b.Clients, tier)
	changes := policy.Each(
		gcloud.GrantedOrRevoked(realtimeKeysReaderRole, member, condition, granting),
		gcloud.GrantedOrRevoked(b.Clients.RealtimeSubscriptionsRolePath(), member, nil, granting),
	)
	if _, err := policy.Edit(ctx, gcloud.ProjectPolicy(b.Clients), changes); err != nil {
		return fmt.Errorf("set whether %s may read the %s tier's realtime keys and hold its gateway instances' Pub/Sub subscriptions: %w", member, tier, err)
	}
	return nil
}

func realtimeRolesGranted(ctx context.Context, b bootstrap.Engine, tier environment.Tier) (bool, error) {
	condition, err := realtimeKeysCondition(ctx, b, tier)
	if err != nil {
		return false, err
	}
	bindings, err := policy.Read(ctx, gcloud.ProjectPolicy(b.Clients))
	if err != nil {
		return false, err
	}
	member := realtimeMember(b.Clients, tier)
	return policy.Holds(bindings, realtimeKeysReaderRole, member, condition) &&
		policy.Holds(bindings, b.Clients.RealtimeSubscriptionsRolePath(), member, nil), nil
}
