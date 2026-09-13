package provider_test

import (
	"context"
	"slices"
	"testing"

	"google.golang.org/protobuf/types/known/structpb"

	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	costv1 "github.com/ocelhq/ocel/pkg/proto/provider/cost/v1"
	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy"
)

func componentNamed(t *testing.T, est *costv1.Estimate, resource, name string) *costv1.CostComponent {
	t.Helper()
	for _, r := range est.GetResources() {
		if r.GetResource() != resource {
			continue
		}
		for _, c := range r.GetComponents() {
			if c.GetName() == name {
				return c
			}
		}
	}
	t.Fatalf("no component %s on %s", name, resource)
	return nil
}

func estimateNamed(t *testing.T, est *costv1.Estimate, resource string) *costv1.ResourceEstimate {
	t.Helper()
	for _, r := range est.GetResources() {
		if r.GetResource() == resource {
			return r
		}
	}
	t.Fatalf("no estimate for %s", resource)
	return nil
}

func TestPriceOfAProductionDeployBehindCloudFront(t *testing.T) {
	client, pricer := costServed(t)

	set, err := client.Shape(context.Background(), &contractv1.ShapeRequest{
		Manifest:    shopManifest(),
		Environment: &environmentv1.Environment{Tier: environmentv1.Tier_TIER_PRODUCTION},
	})
	if err != nil {
		t.Fatalf("Shape() = %v", err)
	}
	est, err := pricer.Price(context.Background(), &costv1.PriceRequest{Resources: set})
	if err != nil {
		t.Fatalf("Price() = %v", err)
	}
	golden(t, "estimate_production_cloudfront", est)

	if est.GetCurrency() != "USD" || est.GetProfile() != costv1.Profile_PROFILE_MODERATE || est.GetRatesVersion() == "" {
		t.Errorf("header = %s %s %q", est.GetCurrency(), est.GetProfile(), est.GetRatesVersion())
	}
	service := estimateNamed(t, est, "project:shop/environment:prod/app:api/aws_ecs_service:api")
	if service.GetMonthlyFixed() != "18.02" {
		t.Errorf("fargate fixed = %s, want 18.02 (0.5 vCPU at 0.04048 and 1 GB at 0.004445 for 730 h)", service.GetMonthlyFixed())
	}
	balancer := estimateNamed(t, est, "project:shop/shared:production/aws_lb:ocel-containers")
	if balancer.GetMonthlyFixed() != "16.43" {
		t.Errorf("alb fixed = %s, want 16.43 (730 h at 0.0225)", balancer.GetMonthlyFixed())
	}
	lambda := "project:shop/environment:prod/app:web/aws_lambda_function:fn--web--entry"
	if got := componentNamed(t, est, lambda, "Requests").GetMonthlyCost(); got != "0.20" {
		t.Errorf("lambda requests = %s, want 0.20 (1M arm64 requests at 0.0000002)", got)
	}
	if got := componentNamed(t, est, lambda, "Duration").GetMonthlyCost(); got != "4.61" {
		t.Errorf("lambda duration = %s, want 4.61 (1M requests of 200 ms at 1769 MB, 345,507.8 GB-s at 0.0000133334)", got)
	}
	instance := "project:shop/environment:prod/aws_rds_cluster_instance:main"
	if got := componentNamed(t, est, instance, "Aurora Serverless v2 capacity"); got.GetMonthlyCost() != "87.60" || got.GetAssumption() != "moderate profile: 730 ACU-hours" {
		t.Errorf("aurora capacity = %v, want 87.60 at the midpoint of 0 and 2 ACU", got)
	}
	if got := componentNamed(t, est, "project:shop/shared:production/aws_dynamodb_table:VarsTable", "Storage"); got.GetMonthlyCost() != "0.00" {
		t.Errorf("vars table storage = %v, want the account's 25 GB allowance to cover 1 GB", got)
	}
	if cov := est.GetCoverage(); cov.GetUnsupported() != 0 || cov.GetNoPrice() != 0 {
		t.Errorf("coverage = %v, want everything the shape lists priced or free", cov)
	}
	if est.GetMonthlyFixed() == "" || est.GetMonthlyUsage() == "" {
		t.Errorf("totals = %q + %q", est.GetMonthlyFixed(), est.GetMonthlyUsage())
	}
}

func TestPriceOutsideTheCardsRegionIsSaidNotGuessed(t *testing.T) {
	_, pricer := costServed(t)

	set := &costv1.ResourceSet{
		Source:    "ocel",
		Scopes:    []*costv1.Scope{{Id: "p", Kind: "project", Name: "shop"}},
		Resources: []*costv1.Resource{{Id: "p/aws_lb:x", Scope: "p", Vendor: "aws", Type: "aws_lb", Name: "x", Region: "eu-west-1"}},
	}
	est, err := pricer.Price(context.Background(), &costv1.PriceRequest{Resources: set})
	if err != nil {
		t.Fatalf("Price() = %v", err)
	}
	if got := estimateNamed(t, est, "p/aws_lb:x"); got.GetStatus() != costv1.ResourceEstimate_STATUS_NO_PRICE || est.GetCoverage().GetNoPrice() != 1 {
		t.Errorf("an ALB in a region the card lacks = %v", got)
	}
}

func TestPriceBehindCloudflareCarriesTheEdgesOwnBill(t *testing.T) {
	client, pricer := costServed(t)

	manifest := shopManifest()
	manifest.Apps = manifest.Apps[:1]
	manifest.Containers = nil
	set, err := client.Shape(context.Background(), &contractv1.ShapeRequest{
		Manifest:    manifest,
		Environment: &environmentv1.Environment{Tier: environmentv1.Tier_TIER_PRODUCTION},
		Edge:        &contractv1.EdgeSelection{Kind: string(cloudflare.Kind)},
	})
	if err != nil {
		t.Fatalf("Shape() = %v", err)
	}
	golden(t, "shape_production_cloudflare", set)
	est, err := pricer.Price(context.Background(), &costv1.PriceRequest{Resources: set})
	if err != nil {
		t.Fatalf("Price() = %v", err)
	}
	golden(t, "estimate_production_cloudflare", est)

	vendors := map[string]int{}
	for _, r := range set.GetResources() {
		vendors[r.GetVendor()]++
	}
	if vendors["cloudflare"] != 5 || vendors["aws"] == 0 {
		t.Errorf("vendors = %v, want the plan, cache, store, writer and entry beside the AWS origin", vendors)
	}
	if typeCounts(set)["aws_lambda_function"] != 1+1+4 {
		t.Errorf("lambdas = %d, want the app's, the upload completer's, and the four that isr, image optimization and the cloudflare edge stand up", typeCounts(set)["aws_lambda_function"])
	}
	if cov := est.GetCoverage(); cov.GetUnsupported() != 0 {
		t.Errorf("coverage = %v, want every cloudflare resource priced by the edge's own card", cov)
	}
	if typeCounts(set)["aws_cloudfront_distribution"] != 0 {
		t.Error("a project fronted by cloudflare stands up no CloudFront distribution")
	}
	egress := componentNamed(t, est, "project:shop/environment:prod/app:web/aws_data_transfer:web", "Data transfer out to internet")
	if egress.GetMonthlyCost() != "0.90" || egress.GetAssumption() != "moderate profile: 10 GB" {
		t.Errorf("origin egress = %v, want 0.90 for the moderate profile's 10 GB at 0.09", egress)
	}
}

func TestPriceOfAFunctionWithUnknownArchitecturesIsNotGuessedAsX86(t *testing.T) {
	_, pricer := costServed(t)

	properties, _ := structpb.NewStruct(map[string]any{"memory_size": 1024})
	set := &costv1.ResourceSet{
		Source: "ocel",
		Scopes: []*costv1.Scope{{Id: "p", Kind: "project", Name: "shop"}},
		Resources: []*costv1.Resource{{
			Id: "p/aws_lambda_function:fn", Scope: "p", Vendor: "aws", Type: "aws_lambda_function", Name: "fn", Region: "us-east-1",
			Properties: properties, Unknown: []string{"architectures"},
		}},
	}
	est, err := pricer.Price(context.Background(), &costv1.PriceRequest{Resources: set})
	if err != nil {
		t.Fatalf("Price() = %v", err)
	}
	fn := estimateNamed(t, est, "p/aws_lambda_function:fn")
	if fn.GetStatus() != costv1.ResourceEstimate_STATUS_NO_PRICE {
		t.Errorf("status = %v, want no price while the architecture is unknown", fn.GetStatus())
	}
	for _, c := range fn.GetComponents() {
		if c.GetMonthlyCost() != "" || !slices.Contains(c.GetDependsOnUnknown(), "architectures") {
			t.Errorf("%s = %v, want it unpriced and naming architectures", c.GetName(), c)
		}
	}
}
