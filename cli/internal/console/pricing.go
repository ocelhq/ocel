package console

import (
	"context"

	"connectrpc.com/connect"

	consolev1 "github.com/ocelhq/ocel/pkg/proto/console/v1"
	"github.com/ocelhq/ocel/pkg/proto/console/v1/consolev1connect"
)

func (c *Client) pricing(accessToken string) consolev1connect.PricingServiceClient {
	return consolev1connect.NewPricingServiceClient(c.http, c.baseURL+connectRoute, c.withSessionHeaders(accessToken), connect.WithProtoJSON())
}

func (c *Client) LookupPrices(ctx context.Context, accessToken string, queries []*consolev1.PriceQuery) (*consolev1.LookupResponse, error) {
	return c.pricing(accessToken).Lookup(ctx, &consolev1.LookupRequest{Queries: queries})
}
