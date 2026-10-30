package console

import (
	"context"

	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	consolev1 "github.com/ocelhq/ocel/pkg/proto/console/v1"
	"github.com/ocelhq/ocel/pkg/proto/console/v1/consolev1connect"
)

func (c *Client) jobs(accessToken string) consolev1connect.JobServiceClient {
	return consolev1connect.NewJobServiceClient(c.http, c.baseURL+connectRoute, c.withSessionHeaders(accessToken))
}

func (c *Client) ResolveRunner(ctx context.Context, accessToken, projectID string, tier environmentv1.Tier, provider string) (*consolev1.ResolveRunnerResponse, error) {
	return c.connectors(accessToken).ResolveRunner(ctx, &consolev1.ResolveRunnerRequest{ProjectId: projectID, Tier: tier, Provider: provider})
}

func (c *Client) DispatchJob(ctx context.Context, accessToken string, req *consolev1.DispatchJobRequest) (*consolev1.DispatchedJob, error) {
	dispatched, err := c.jobs(accessToken).Dispatch(ctx, req)
	if err != nil {
		return nil, err
	}
	return dispatched.GetJob(), nil
}

func (c *Client) GetDispatchedJob(ctx context.Context, accessToken, projectID, jobID string) (*consolev1.DispatchedJob, error) {
	read, err := c.jobs(accessToken).Get(ctx, &consolev1.GetJobRequest{ProjectId: projectID, JobId: jobID})
	if err != nil {
		return nil, err
	}
	return read.GetJob(), nil
}
