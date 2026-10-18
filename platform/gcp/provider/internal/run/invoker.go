package run

import (
	"context"

	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/policy"
)

func GrantInvoker(ctx context.Context, c *gcloud.Clients, service, member string) error {
	_, err := policy.Edit(ctx, gcloud.RunServicePolicy(c, service), policy.Grant(InvokerRole, member, nil))
	return err
}
