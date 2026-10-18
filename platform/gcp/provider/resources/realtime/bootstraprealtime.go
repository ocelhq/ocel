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
		Description: "the identity the realtime gateways of the " + string(tier) + " tier run as, which may read the tier's realtime keys and nothing else",
		Ungranted:   "it exists, and it may not read the tier's realtime keys, so a gateway mounting them would never start",
		Grant:       grantRealtimeKeysRead,
		Forget:      forgetRealtimeKeysRead,
		Granted:     realtimeKeysReadGranted,
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

func grantRealtimeKeysRead(ctx context.Context, b bootstrap.Engine, tier environment.Tier) error {
	return bindRealtimeKeysRead(ctx, b, tier, true)
}

func forgetRealtimeKeysRead(ctx context.Context, b bootstrap.Engine, tier environment.Tier) error {
	return bindRealtimeKeysRead(ctx, b, tier, false)
}

func bindRealtimeKeysRead(ctx context.Context, b bootstrap.Engine, tier environment.Tier, granting bool) error {
	condition, err := realtimeKeysCondition(ctx, b, tier)
	if err != nil {
		return err
	}
	member := realtimeMember(b.Clients, tier)
	if err := gcloud.BindProjectRole(ctx, b.Clients, member, realtimeKeysReaderRole, condition, granting); err != nil {
		return fmt.Errorf("set whether %s may read the %s tier's realtime keys: %w", member, tier, err)
	}
	return nil
}

func realtimeKeysReadGranted(ctx context.Context, b bootstrap.Engine, tier environment.Tier) (bool, error) {
	condition, err := realtimeKeysCondition(ctx, b, tier)
	if err != nil {
		return false, err
	}
	return gcloud.ProjectRoleGranted(ctx, b.Clients, realtimeMember(b.Clients, tier), realtimeKeysReaderRole, condition)
}
