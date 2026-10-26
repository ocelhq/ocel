package run

import (
	"context"
	"fmt"

	"google.golang.org/api/googleapi"
	run "google.golang.org/api/run/v2"

	"github.com/ocelhq/ocel/platform/gcp/provider/googleretry"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
)

func (p *Services) ReadRevisionAccount(ctx context.Context, service, revision string) (string, bool, error) {
	clients, services, err := p.OpenRun(ctx)
	if err != nil {
		return "", false, err
	}
	read, err := googleretry.Attempted(ctx, func(call ...googleapi.CallOption) (*run.GoogleCloudRunV2Revision, error) {
		return services.Projects.Locations.Services.Revisions.Get(clients.ServicePath(service) + "/revisions/" + revision).Context(ctx).Do(call...)
	})
	if gcloud.Absent(err) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("read which account revision %s of %s runs as: %w", revision, service, err)
	}
	return read.ServiceAccount, true, nil
}
