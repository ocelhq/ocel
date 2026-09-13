package pricing_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/ocelhq/ocel/pkg/proto/provider/cost/v1/costv1connect"
	"github.com/ocelhq/ocel/pricing"

	costv1 "github.com/ocelhq/ocel/pkg/proto/provider/cost/v1"
)

func served(t *testing.T, opts pricing.Options) (*httptest.Server, costv1connect.CostServiceClient) {
	t.Helper()
	store, err := pricing.Cards()
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(pricing.Handler(store, opts))
	t.Cleanup(server.Close)
	return server, costv1connect.NewCostServiceClient(server.Client(), server.URL)
}

func threeVendors() *costv1.ResourceSet {
	return &costv1.ResourceSet{
		Source: "pulumi",
		Scopes: []*costv1.Scope{{Id: "s", Kind: "stack", Name: "shop-dev"}},
		Resources: []*costv1.Resource{
			{Id: "s/aws_s3_bucket:uploads", Scope: "s", Vendor: "aws", Type: "aws_s3_bucket", Name: "uploads", Region: "us-east-1"},
			{Id: "s/google_storage_bucket:assets", Scope: "s", Vendor: "gcp", Type: "google_storage_bucket", Name: "assets", Region: "europe-west1"},
			{Id: "s/cloudflare_r2_bucket:cache", Scope: "s", Vendor: "cloudflare", Type: "cloudflare_r2_bucket", Name: "cache"},
		},
	}
}

func TestHealthzAnswersWithoutATokenOrAStore(t *testing.T) {
	server, _ := served(t, pricing.Options{Tokens: []string{"secret"}})

	resp, err := server.Client().Get(server.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("GET /healthz = %d, want 200 with the allowlist on", resp.StatusCode)
	}
}

func TestOneCallPricesEveryVendorTheServiceCarries(t *testing.T) {
	_, pricer := served(t, pricing.Options{})

	est, err := pricer.Price(context.Background(), &costv1.PriceRequest{Resources: threeVendors()})
	if err != nil {
		t.Fatalf("Price() = %v", err)
	}

	if got := est.GetCoverage().GetUnsupported(); got != 0 {
		t.Errorf("%d of three vendors' resources went unpriced: %v", got, est.GetCoverage().GetUnsupportedTypes())
	}
	for _, r := range est.GetResources() {
		if r.GetStatus() != costv1.ResourceEstimate_STATUS_PRICED {
			t.Errorf("%s is %s, want a price", r.GetResource(), r.GetStatus())
		}
	}
	for _, note := range []string{
		"CloudFront is priced at its United States rates whichever price class the distribution carries",
		"the forwarding rule is priced as the project's first five, which share one hourly charge",
		"Workers, Durable Objects and R2 carry one global price, so the estimate does not move with the region",
	} {
		if !slices.Contains(est.GetNotes(), note) {
			t.Errorf("notes = %v, want the union over every vendor", est.GetNotes())
		}
	}
}

func TestAForeignSourceIsPricedAsSent(t *testing.T) {
	_, pricer := served(t, pricing.Options{})

	set := threeVendors()
	set.Source = "sst"
	if _, err := pricer.Price(context.Background(), &costv1.PriceRequest{Resources: set}); err != nil {
		t.Fatalf("Price() of an sst inventory = %v", err)
	}
}

func TestAnAllowlistedListenerPricesOnlyForABearerItKnows(t *testing.T) {
	server, _ := served(t, pricing.Options{Tokens: []string{"alpha", "beta"}})
	req := &costv1.PriceRequest{Resources: threeVendors()}

	for _, header := range []string{"", "Bearer gamma", "alpha", "Basic alpha"} {
		client := costv1connect.NewCostServiceClient(server.Client(), server.URL,
			connect.WithInterceptors(bearer(header)))
		_, err := client.Price(context.Background(), req)
		if connect.CodeOf(err) != connect.CodeUnauthenticated {
			t.Errorf("Price() with %q = %v, want it refused", header, err)
		}
	}

	client := costv1connect.NewCostServiceClient(server.Client(), server.URL,
		connect.WithInterceptors(bearer("Bearer beta")))
	if _, err := client.Price(context.Background(), req); err != nil {
		t.Errorf("Price() with an allowlisted bearer = %v", err)
	}
}

func bearer(header string) connect.UnaryInterceptorFunc {
	return func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			if header != "" {
				req.Header().Set("Authorization", header)
			}
			return next(ctx, req)
		}
	}
}

func TestAUsageFileThePricerCannotReadIsTheCallersFault(t *testing.T) {
	_, pricer := served(t, pricing.Options{})

	usage, err := structpb.NewStruct(map[string]any{"storage_gb": "plenty"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = pricer.Price(context.Background(), &costv1.PriceRequest{
		Resources: threeVendors(),
		Usage:     &costv1.Usage{Resources: map[string]*structpb.Struct{"s/aws_s3_bucket:uploads": usage}},
	})
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("Price() with an unreadable usage file = %v, want it named as the caller's", err)
	}
}
