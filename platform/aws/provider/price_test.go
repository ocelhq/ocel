package provider_test

import (
	"context"
	"testing"

	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	costv1 "github.com/ocelhq/ocel/pkg/proto/provider/cost/v1"
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

	if est.GetCurrency() != "USD" || est.GetProfile() != "moderate" || est.GetRatesVersion() == "" {
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
	cluster := "project:shop/environment:prod/aws_rds_cluster:main"
	if got := componentNamed(t, est, cluster, "Aurora Serverless v2 capacity"); got.GetMonthlyCost() != "87.60" || got.GetAssumption() != "moderate profile: 730 ACU-hours" {
		t.Errorf("aurora capacity = %v, want 87.60 at the midpoint of 0 and 2 ACU", got)
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
