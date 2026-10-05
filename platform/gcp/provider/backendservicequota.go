package gcp

import (
	"context"
	"fmt"

	"google.golang.org/api/compute/v1"
	"google.golang.org/api/googleapi"

	"github.com/ocelhq/ocel/platform/gcp/provider/edges/alb"
)

const globalExternalManagedBackendServices = "GLOBAL_EXTERNAL_MANAGED_BACKEND_SERVICES"

func (p *Provider) ReadBackendServiceQuota(ctx context.Context) (alb.BackendServiceQuota, bool, error) {
	clients, err := p.openClients(ctx)
	if err != nil {
		return alb.BackendServiceQuota{}, false, err
	}
	engine, err := clients.Compute()
	if err != nil {
		return alb.BackendServiceQuota{}, false, err
	}
	project, err := attempted(ctx, func(call ...googleapi.CallOption) (*compute.Project, error) {
		return engine.Projects.Get(clients.project).Fields("quotas").Context(ctx).Do(call...)
	})
	if err != nil {
		return alb.BackendServiceQuota{}, false, fmt.Errorf("read the quotas of project %s: %w", clients.project, err)
	}
	for _, quota := range project.Quotas {
		if quota.Metric == globalExternalManagedBackendServices {
			return alb.BackendServiceQuota{Usage: quota.Usage, Limit: quota.Limit}, true, nil
		}
	}
	return alb.BackendServiceQuota{}, false, nil
}
