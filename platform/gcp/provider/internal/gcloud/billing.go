package gcloud

import (
	"context"

	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/gcp/provider/googleretry"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/credential"
	"github.com/ocelhq/ocel/platform/gcp/provider/ports"
)

const (
	serviceUsageAPI   = "serviceusage.googleapis.com"
	userProjectDenied = "USER_PROJECT_DENIED"
)

func (c *Clients) RefuseBillingDenied(ctx context.Context) error {
	billed := c.QuotaProject()
	if billed == "" {
		return nil
	}
	service, err := c.Services()
	if err != nil {
		return err
	}
	_, err = googleretry.Attempted(ctx, service.Services.Get("projects/"+c.Project()+"/services/"+serviceUsageAPI).Context(ctx).Do)
	if !isBillingDenied(err) {
		return nil
	}
	return refusal.Refuse(refusal.CodeDenied, "this credential's calls are billed to project %s, and it lacks %s there; grant it %s, or set %s to the project to bill instead",
		billed, credential.BillingPermission, credential.BillingRole, ports.QuotaProjectEnvVar)
}

func isBillingDenied(err error) bool { return AnsweredReason(err) == userProjectDenied }
