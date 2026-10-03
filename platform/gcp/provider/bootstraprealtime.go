package gcp

import (
	"context"
	"fmt"

	"google.golang.org/api/cloudresourcemanager/v1"

	"github.com/ocelhq/ocel/pkg/environment"
)

const (
	realtimeKeysReaderRole = "roles/secretmanager.secretAccessor"
	secretVersionType      = "secretmanager.googleapis.com/SecretVersion"
)

func (b bootstrap) realtimePurpose(tier environment.Tier) accountPurpose {
	return accountPurpose{
		displayName: "ocel " + string(tier) + " realtime",
		description: "the identity the realtime gateways of the " + string(tier) + " tier run as, which may read the tier's realtime keys and nothing else",
		ungranted:   "it exists, and it may not read the tier's realtime keys, so a gateway mounting them would never start",
		grant:       b.grantRealtimeKeysRead,
		forget:      b.forgetRealtimeKeysRead,
		granted:     b.realtimeKeysReadGranted,
	}
}

func realtimeMember(c *clients, tier environment.Tier) string {
	return "serviceAccount:" + c.RealtimeAccountEmail(tier)
}

func (b bootstrap) realtimeKeysCondition(ctx context.Context, tier environment.Tier) (*cloudresourcemanager.Expr, error) {
	number, err := b.clients.ReadProjectNumber(ctx)
	if err != nil {
		return nil, err
	}
	return &cloudresourcemanager.Expr{
		Title: "ocel " + string(b.clients.Namespace()) + " " + string(tier) + " realtime keys",
		Expression: fmt.Sprintf("resource.type == %q && resource.name.startsWith(%q)",
			secretVersionType, fmt.Sprintf("projects/%d/secrets/%s", number, b.clients.RealtimeKeysSecretPrefix(tier))),
	}, nil
}

func (b bootstrap) grantRealtimeKeysRead(ctx context.Context, tier environment.Tier) error {
	return b.bindRealtimeKeysRead(ctx, tier, true)
}

func (b bootstrap) forgetRealtimeKeysRead(ctx context.Context, tier environment.Tier) error {
	return b.bindRealtimeKeysRead(ctx, tier, false)
}

func (b bootstrap) bindRealtimeKeysRead(ctx context.Context, tier environment.Tier, granting bool) error {
	condition, err := b.realtimeKeysCondition(ctx, tier)
	if err != nil {
		return err
	}
	member := realtimeMember(b.clients, tier)
	if err := b.clients.bindProjectRole(ctx, member, realtimeKeysReaderRole, condition, granting); err != nil {
		return fmt.Errorf("set whether %s may read the %s tier's realtime keys: %w", member, tier, err)
	}
	return nil
}

func (b bootstrap) realtimeKeysReadGranted(ctx context.Context, tier environment.Tier) (bool, error) {
	condition, err := b.realtimeKeysCondition(ctx, tier)
	if err != nil {
		return false, err
	}
	return b.clients.projectRoleGranted(ctx, realtimeMember(b.clients, tier), realtimeKeysReaderRole, condition)
}
