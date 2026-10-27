package console

import (
	"context"

	consolev1 "github.com/ocelhq/ocel/pkg/proto/console/v1"
	"github.com/ocelhq/ocel/pkg/proto/console/v1/consolev1connect"
)

func (c *Client) targets(accessToken string) consolev1connect.TargetServiceClient {
	return consolev1connect.NewTargetServiceClient(c.http, c.baseURL+connectRoute, c.withSessionHeaders(accessToken))
}

func (c *Client) IssueCredential(ctx context.Context, accessToken string, req *consolev1.IssueCredentialRequest) (*consolev1.IssueCredentialResponse, error) {
	return c.targets(accessToken).IssueCredential(ctx, req)
}

func (c *Client) PrepareTarget(ctx context.Context, accessToken string, req *consolev1.PrepareTargetRequest) (*consolev1.PrepareTargetResponse, error) {
	return c.targets(accessToken).Prepare(ctx, req)
}

func (c *Client) RecordTargetTier(ctx context.Context, accessToken string, req *consolev1.RecordTargetTierRequest) error {
	_, err := c.targets(accessToken).RecordTier(ctx, req)
	return err
}

func (c *Client) ForgetTargetTier(ctx context.Context, accessToken string, req *consolev1.ForgetTargetTierRequest) error {
	_, err := c.targets(accessToken).ForgetTier(ctx, req)
	return err
}
