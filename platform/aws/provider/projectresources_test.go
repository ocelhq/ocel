package aws

import (
	"context"
	"slices"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/router"
	"github.com/ocelhq/ocel/platform/aws/provider/account"
	"github.com/ocelhq/ocel/platform/aws/provider/dns"
	"github.com/ocelhq/ocel/platform/aws/provider/edges/alb"
	"github.com/ocelhq/ocel/platform/aws/provider/edges/apigateway"
	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy"
)

const (
	inventoryRegion    = "eu-west-1"
	inventoryNamespace = account.Namespace("ocel2")
	shopECRImage       = "123456789012.dkr.ecr.eu-west-1.amazonaws.com/ocel2/prod/shop.web:sha256-own"
)

func inventoryOrigin(context.Context) (cloudflare.OriginScope, error) {
	return cloudflare.OriginScope{Vendor: string(Vendor), ID: "123456789012/" + inventoryRegion}, nil
}

func listShopResources(t *testing.T, store records.Store, req provider.ProjectResourcesRequest) []provider.PhysicalResource {
	t.Helper()
	if req.Project == "" {
		req.Project = "shop"
	}
	if req.Tier == "" {
		req.Tier = environment.TierProduction
	}
	listed, err := listProjectResources(context.Background(), store, inventoryNamespace, inventoryRegion, inventoryOrigin, req)
	if err != nil {
		t.Fatalf("listProjectResources() = %v", err)
	}
	return listed
}

func TestProjectResourcesNameTheImageRepositoryOfAProjectThatPushedImages(t *testing.T) {
	t.Parallel()

	listed := listShopResources(t, fake.NewKeyValues(), provider.ProjectResourcesRequest{Images: []string{
		shopECRImage,
		"ghcr.io/acme/shop:1",
		"123456789012.dkr.ecr.eu-west-1.amazonaws.com/ocel2/prod/blog.web:sha256-other",
		"123456789012.dkr.ecr.eu-west-1.amazonaws.com/ocel2/preview/shop.web:sha256-other-tier",
		"123456789012.dkr.ecr.eu-west-1.amazonaws.com/ocel/prod/shop.web:sha256-other-namespace",
	}})

	want := []provider.PhysicalResource{{Kind: "AWS::ECR::Repository", ID: "ocel2/prod/shop.web", Location: inventoryRegion}}
	if !slices.Equal(listed, want) {
		t.Errorf("listProjectResources() = %+v, want only shop's own repository %+v", listed, want)
	}
}

func TestProjectResourcesNameNoImageRepositoryForAProjectThatPushedNone(t *testing.T) {
	t.Parallel()

	if listed := listShopResources(t, fake.NewKeyValues(), provider.ProjectResourcesRequest{}); len(listed) != 0 {
		t.Errorf("listProjectResources() = %+v, want none: a project on functions pushes no image", listed)
	}
}

func TestProjectResourcesNameTheCertificatesOcelRequested(t *testing.T) {
	t.Parallel()

	requested := "arn:aws:acm:us-east-1:123456789012:certificate/requested"
	listed := listShopResources(t, fake.NewKeyValues(), provider.ProjectResourcesRequest{Certificates: []provider.Certificate{
		{ID: requested, Requested: true},
		{ID: "arn:aws:acm:us-east-1:123456789012:certificate/brought"},
	}})

	want := []provider.PhysicalResource{{Kind: "AWS::CertificateManager::Certificate", ID: requested, Location: "us-east-1"}}
	if !slices.Equal(listed, want) {
		t.Errorf("listProjectResources() = %+v, want only the certificate Ocel requested %+v", listed, want)
	}
}

func TestProjectResourcesNameTheRoute53RecordsOcelWrote(t *testing.T) {
	t.Parallel()

	written := edge.Record{Name: "shop.example.com", Type: "CNAME", Value: "d111.cloudfront.net"}
	listed := listShopResources(t, fake.NewKeyValues(), provider.ProjectResourcesRequest{
		DNSKind:        provider.DNSKind(dns.KindRoute53),
		WrittenRecords: []edge.Record{written},
	})

	want := []provider.PhysicalResource{{Kind: "AWS::Route53::RecordSet", ID: written.String(), Location: "global"}}
	if !slices.Equal(listed, want) {
		t.Errorf("listProjectResources() = %+v, want %+v", listed, want)
	}
}

func TestProjectResourcesNameNoRoute53RecordWhereAnotherDNSHoldsThem(t *testing.T) {
	t.Parallel()

	listed := listShopResources(t, fake.NewKeyValues(), provider.ProjectResourcesRequest{
		DNSKind:        provider.DNSKind(cloudflare.Kind),
		WrittenRecords: []edge.Record{{Name: "shop.example.com", Type: "CNAME", Value: "d111.cloudfront.net"}},
	})

	for _, resource := range listed {
		if resource.Kind == "AWS::Route53::RecordSet" {
			t.Errorf("listProjectResources() = %+v, want no Route 53 record: Cloudflare holds them", listed)
		}
	}
}

func TestProjectResourcesNameTheRESTAPIRecordedForTheProject(t *testing.T) {
	t.Parallel()

	listed := listShopResources(t, fake.NewKeyValues(), provider.ProjectResourcesRequest{
		EdgeKind:  apigateway.Kind,
		EdgeStack: edge.StackState{Private: edge.Own(map[string]string{"api": "a1b2c3d4e5"})},
	})

	want := []provider.PhysicalResource{{Kind: "AWS::ApiGateway::RestApi", ID: "a1b2c3d4e5", Location: inventoryRegion}}
	if !slices.Equal(listed, want) {
		t.Errorf("listProjectResources() = %+v, want %+v", listed, want)
	}
}

func TestProjectResourcesNameTheLoadBalancerRulesAndCertificatesRecordedForTheProject(t *testing.T) {
	t.Parallel()

	rule := "arn:aws:elasticloadbalancing:eu-west-1:123456789012:listener-rule/app/ocel-public-production/abc/def/ghi"
	cert := "arn:aws:acm:eu-west-1:123456789012:certificate/shop"
	routed := router.NewStackState(edge.StackState{Private: edge.Own(map[string]any{"hosts": map[string]any{
		"shop.example.com": map[string]any{"app": "web", "pointer": "@production", "rule": rule, "certificate": cert, "certificateRequested": true},
	}})})
	listed := listShopResources(t, fake.NewKeyValues(), provider.ProjectResourcesRequest{
		EdgeKind:     cloudflare.Kind,
		Routers:      map[router.Kind]router.StackState{alb.Kind: routed},
		Certificates: []provider.Certificate{{ID: cert, Requested: true}},
	})

	want := []provider.PhysicalResource{
		{Kind: "AWS::ElasticLoadBalancingV2::ListenerRule", ID: rule, Location: inventoryRegion},
		{Kind: "AWS::ElasticLoadBalancingV2::ListenerCertificate", ID: cert, Location: inventoryRegion},
		{Kind: "AWS::CertificateManager::Certificate", ID: cert, Location: inventoryRegion},
	}
	if !slices.Equal(listed, want) {
		t.Errorf("listProjectResources() = %+v, want %+v, each named once", listed, want)
	}
}
