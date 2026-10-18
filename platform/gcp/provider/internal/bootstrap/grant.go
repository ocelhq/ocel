package bootstrap

import (
	"context"
	"fmt"
	"time"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/platform/gcp/provider/cost"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
)

const (
	NetworkUserRole = "roles/compute.networkUser"
	runAgentDomain  = "@serverless-robot-prod.iam.gserviceaccount.com"
)

var grantKind = KindOps{
	noun:          "project grant of",
	terraform:     cost.ProjectIAMMemberType,
	emulatorSkips: true,
	survey: func(ctx context.Context, b Engine, _ environment.Tier, target Resource) (Presence, error) {
		return presenceFrom(b.projectGrantBound(ctx, target.Name))
	},
	Make: func(ctx context.Context, b Engine, _ Survey, target Resource) error {
		return b.bindProjectGrant(ctx, target.Name, true)
	},
	Take: func(ctx context.Context, b Engine, _ Survey, target Resource) (time.Duration, error) {
		return 0, b.bindProjectGrant(ctx, target.Name, false)
	},
}

var projectGrantees = map[string]string{
	NetworkUserRole: runAgentDomain,
}

func (b Engine) readProjectGrantee(ctx context.Context, role string) (string, error) {
	domain, known := projectGrantees[role]
	if !known {
		return "", fmt.Errorf("no service agent is granted %s on the project", role)
	}
	return b.Clients.ReadServiceAgent(ctx, domain)
}

func (b Engine) bindProjectGrant(ctx context.Context, role string, granting bool) error {
	agent, err := b.readProjectGrantee(ctx, role)
	if err != nil {
		return err
	}
	return gcloud.BindProjectRole(ctx, b.Clients, agent, role, nil, granting)
}

func (b Engine) projectGrantBound(ctx context.Context, role string) (bool, error) {
	agent, err := b.readProjectGrantee(ctx, role)
	if err != nil {
		return false, err
	}
	return gcloud.ProjectRoleGranted(ctx, b.Clients, agent, role, nil)
}
