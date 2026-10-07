package console

import (
	"context"

	"connectrpc.com/connect"

	consolev1 "github.com/ocelhq/ocel/pkg/proto/console/v1"
	"github.com/ocelhq/ocel/pkg/proto/console/v1/consolev1connect"
)

func (c *Client) deployments(accessToken string) consolev1connect.DeploymentServiceClient {
	return consolev1connect.NewDeploymentServiceClient(c.http, c.baseURL, connect.WithInterceptors(connect.UnaryInterceptorFunc(
		func(next connect.UnaryFunc) connect.UnaryFunc {
			return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
				req.Header().Set("Authorization", "Bearer "+accessToken)
				req.Header().Set("User-Agent", c.userAgent)
				return next(ctx, req)
			}
		},
	)))
}

func (c *Client) ReportDeployment(ctx context.Context, accessToken, projectID string, deployment *consolev1.Deployment) error {
	_, err := c.deployments(accessToken).Report(ctx, &consolev1.ReportRequest{ProjectId: projectID, Deployment: deployment})
	return err
}

func (c *Client) RecordEnvironmentEvent(ctx context.Context, accessToken, projectID string, event *consolev1.EnvironmentEvent) error {
	_, err := c.deployments(accessToken).RecordEnvironmentEvent(ctx, &consolev1.RecordEnvironmentEventRequest{ProjectId: projectID, Event: event})
	return err
}
