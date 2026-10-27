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
