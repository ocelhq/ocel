package gcp_test

import (
	"context"
	"strings"
	"testing"

	"google.golang.org/protobuf/types/known/structpb"

	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	costv1 "github.com/ocelhq/ocel/pkg/proto/provider/cost/v1"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/alb"
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

func estimateOfType(t *testing.T, est *costv1.Estimate, set *costv1.ResourceSet, typ, scope string) *costv1.ResourceEstimate {
	t.Helper()
	for _, resource := range set.GetResources() {
		if resource.GetType() != typ || resource.GetScope() != scope {
			continue
		}
		for _, r := range est.GetResources() {
			if r.GetResource() == resource.GetId() {
				return r
			}
		}
	}
	t.Fatalf("no %s under %s", typ, scope)
	return nil
}

func TestPriceOfAProductionDeployBehindTheLoadBalancer(t *testing.T) {
	client, pricer := costServed(t)

	set, err := client.Shape(context.Background(), &contractv1.ShapeRequest{
		Manifest:    shopManifest(),
		Environment: &environmentv1.Environment{Tier: environmentv1.Tier_TIER_PRODUCTION},
		Edge:        &contractv1.EdgeSelection{Kind: string(alb.Kind)},
	})
	if err != nil {
		t.Fatalf("Shape() = %v", err)
	}
	est, err := pricer.Price(context.Background(), &costv1.PriceRequest{Resources: set})
	if err != nil {
		t.Fatalf("Price() = %v", err)
	}
	golden(t, "estimate_production_alb", est)

	container := estimateOfType(t, est, set, "google_cloud_run_v2_service", "project:shop/environment:prod/app:api")
	if container.GetMonthlyFixed() != "49.93" {
		t.Errorf("container service fixed = %s, want 49.93 (one warm vCPU at 0.000018 and 0.5 GiB at 0.000002 for 2,628,000 s)", container.GetMonthlyFixed())
	}
	if container.GetMonthlyUsage() != "0.00" {
		t.Errorf("container service usage = %s, want none: instance-based billing charges no request", container.GetMonthlyUsage())
	}
	function := estimateOfType(t, est, set, "google_cloud_run_v2_service", "project:shop/environment:prod/app:web")
	if got := componentNamed(t, est, function.GetResource(), "Requests").GetMonthlyCost(); got != "0.40" {
		t.Errorf("requests = %s, want 0.40 (1M at 0.0000004)", got)
	}
	if got := componentNamed(t, est, function.GetResource(), "CPU during requests").GetMonthlyCost(); got != "4.80" {
		t.Errorf("cpu = %s, want 4.80 (200,000 vCPU-s at 0.000024)", got)
	}
	if function.GetMonthlyFixed() != "0.00" {
		t.Errorf("a serverless service stands for %s a month, want nothing", function.GetMonthlyFixed())
	}
	rule := estimateOfType(t, est, set, "google_compute_global_forwarding_rule", "project:shop/shared:production")
	if rule.GetMonthlyFixed() != "18.25" {
		t.Errorf("forwarding rule fixed = %s, want 18.25 (730 h at 0.025)", rule.GetMonthlyFixed())
	}
	for _, r := range est.GetResources() {
		for _, c := range r.GetComponents() {
			if c.GetName() == "Data transfer out to internet" {
				t.Errorf("%s bills internet egress behind a load balancer that carries it instead", r.GetResource())
			}
		}
	}
	if cov := est.GetCoverage(); cov.GetUnsupported() != 0 || cov.GetNoPrice() != 0 {
		t.Errorf("coverage = %v, want everything the shape lists priced or free", cov)
	}
}

func TestPriceOfADirectDeployBillsEgressOnTheService(t *testing.T) {
	client, pricer := costServed(t)

	set, err := client.Shape(context.Background(), &contractv1.ShapeRequest{
		Manifest:    shopManifest(),
		Environment: &environmentv1.Environment{Tier: environmentv1.Tier_TIER_PRODUCTION},
	})
	if err != nil {
		t.Fatalf("Shape() = %v", err)
	}
	est, err := pricer.Price(context.Background(), &costv1.PriceRequest{Resources: set, Usage: &costv1.Usage{Profile: costv1.Profile_PROFILE_HEAVY}})
	if err != nil {
		t.Fatalf("Price() = %v", err)
	}
	function := estimateOfType(t, est, set, "google_cloud_run_v2_service", "project:shop/environment:prod/app:web")
	if got := componentNamed(t, est, function.GetResource(), "Data transfer out to internet"); got.GetMonthlyCost() != "12.00" || got.GetAssumption() != "heavy profile: 100 GiB" {
		t.Errorf("egress = %v, want 12.00 for the heavy profile's 100 GiB at 0.12", got)
	}
}

func TestPriceOfAServiceOutsideTheNamedRegionsFallsBackToTierOne(t *testing.T) {
	_, pricer := costServed(t)

	properties, _ := structpb.NewStruct(map[string]any{
		"ingress": "INGRESS_TRAFFIC_INTERNAL_LOAD_BALANCER",
		"template": map[string]any{
			"scaling":    map[string]any{"min_instance_count": 1},
			"containers": []any{map[string]any{"resources": map[string]any{"cpu_idle": false, "limits": map[string]any{"cpu": "1", "memory": "512Mi"}}}},
		},
	})
	set := &costv1.ResourceSet{
		Source: "ocel",
		Scopes: []*costv1.Scope{{Id: "p", Kind: "project", Name: "shop"}},
		Resources: []*costv1.Resource{{
			Id: "p/svc", Scope: "p", Vendor: "gcp", Type: "google_cloud_run_v2_service", Name: "svc", Region: "europe-west9", Properties: properties,
		}},
	}
	est, err := pricer.Price(context.Background(), &costv1.PriceRequest{Resources: set})
	if err != nil {
		t.Fatalf("Price() = %v", err)
	}
	if got := componentNamed(t, est, "p/svc", "CPU, always allocated"); got.GetMonthlyCost() != "47.30" {
		t.Errorf("cpu = %v, want 47.30 at the tier-1 rate", got)
	}
	if len(est.GetNotes()) == 0 || !strings.Contains(est.GetNotes()[0], "tier-1") {
		t.Errorf("notes = %v, want the fallback's note first", est.GetNotes())
	}
}
