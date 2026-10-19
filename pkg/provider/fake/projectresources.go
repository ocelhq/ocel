package fake

import (
	"context"

	"github.com/ocelhq/ocel/pkg/provider"
)

func (p *Provider) ListProjectResources(_ context.Context, req provider.ProjectResourcesRequest) ([]provider.PhysicalResource, error) {
	listed := make([]provider.PhysicalResource, 0, len(req.WrittenRecords))
	for _, record := range req.WrittenRecords {
		listed = append(listed, provider.PhysicalResource{Kind: "fake:dns-record", ID: record.String()})
	}
	return listed, nil
}
