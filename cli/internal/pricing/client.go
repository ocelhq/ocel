package pricing

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"connectrpc.com/connect"

	costv1 "github.com/ocelhq/ocel/pkg/proto/provider/cost/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/cost/v1/costv1connect"
)

const (
	DefaultBaseURL = "https://pricing.ocel.dev"
	URLEnvVar      = "OCEL_PRICING_URL"
	TokenEnvVar    = "OCEL_PRICING_TOKEN"

	timeout = 30 * time.Second
)

func ResolveBaseURL(flag string) string {
	if held := strings.TrimSpace(flag); held != "" {
		return strings.TrimRight(held, "/")
	}
	if held := strings.TrimSpace(os.Getenv(URLEnvVar)); held != "" {
		return strings.TrimRight(held, "/")
	}
	if os.Getenv("OCEL_DEV") != "" {
		return "http://localhost:8090"
	}
	return DefaultBaseURL
}

type Client struct {
	url   string
	inner costv1connect.CostServiceClient
}

func New(baseURL, token string) *Client {
	var opts []connect.ClientOption
	if token != "" {
		opts = append(opts, connect.WithInterceptors(bearer(token)))
	}
	return &Client{url: baseURL, inner: costv1connect.NewCostServiceClient(&http.Client{Timeout: timeout}, baseURL, opts...)}
}

func (c *Client) Price(ctx context.Context, req *costv1.PriceRequest) (*costv1.Estimate, error) {
	estimate, err := c.inner.Price(ctx, req)
	if err == nil {
		return estimate, nil
	}
	if connect.CodeOf(err) == connect.CodeInvalidArgument {
		return nil, err
	}
	return nil, fmt.Errorf("the pricer at %s did not answer: %w. Point --pricing-url or %s at one you can reach, or run your own with `docker compose up pricing`", c.url, err, URLEnvVar)
}

func bearer(token string) connect.UnaryInterceptorFunc {
	return func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			req.Header().Set("Authorization", "Bearer "+token)
			return next(ctx, req)
		}
	}
}
