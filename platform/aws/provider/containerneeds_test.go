package aws_test

import (
	"errors"
	"slices"
	"testing"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/conformance"
	"github.com/ocelhq/ocel/pkg/provider/providerserver"
	aws "github.com/ocelhq/ocel/platform/aws/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/edges/cloudfront"
)

var nextContainerNeeds = []edge.Need{edge.NeedEdgeCache, edge.NeedStreaming, edge.NeedRevalidation}

func TestANextContainerBehindCloudFrontServingPrerendersDeploysWithNoWaiver(t *testing.T) {
	p := aws.NewProvider(aws.Options{Region: "us-east-1"}, nil, awssdk.Config{Region: "us-east-1"}, defaultNamespace)

	verdict, err := conformance.CheckContainerNeeds(t, p, cloudfront.Kind, nil, nextContainerNeeds)
	if err != nil {
		t.Fatalf("the need check = %v, want it to pass: CloudFront stores and purges a Next container's tagged pages", err)
	}
	if !slices.Contains(verdict.InEffect, edge.NeedEdgeCache) {
		t.Errorf("needs in effect = %v, want %s", verdict.InEffect, edge.NeedEdgeCache)
	}
}

func TestANextContainerBehindCloudFrontStillNeedsAWaiverForTheEdgeRuntime(t *testing.T) {
	p := aws.NewProvider(aws.Options{Region: "us-east-1"}, nil, awssdk.Config{Region: "us-east-1"}, defaultNamespace)

	_, err := conformance.CheckContainerNeeds(t, p, cloudfront.Kind, nil, []edge.Need{edge.NeedEdgeRuntime})
	if unsupported := (*providerserver.UnsupportedNeedError)(nil); !errors.As(err, &unsupported) {
		t.Errorf("the need check = %v, want %s refused: CloudFront runs no code in front of a container", err, edge.NeedEdgeRuntime)
	}
}

func TestANextContainerForwardedPastCloudflareNeedsAWaiverForTheEdgeCache(t *testing.T) {
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "conformance")
	p := aws.NewProvider(aws.Options{Region: "us-east-1"}, nil, awssdk.Config{Region: "us-east-1"}, defaultNamespace)

	_, err := conformance.CheckContainerNeeds(t, p, "cloudflare", nil, nextContainerNeeds)
	if unsupported := (*providerserver.UnsupportedNeedError)(nil); !errors.As(err, &unsupported) || unsupported.Need != edge.NeedEdgeCache {
		t.Errorf("the need check = %v, want %s refused: the Worker forwards a container's requests past its cache", err, edge.NeedEdgeCache)
	}
	if _, err := conformance.CheckContainerNeeds(t, p, "cloudflare", nil, nextContainerNeeds, string(edge.NeedEdgeCache)); err != nil {
		t.Errorf("the need check with %s waived = %v, want it to pass", edge.NeedEdgeCache, err)
	}
}

func TestANextContainerBehindACachingCloudflareIsServedEveryNeedByTheWorkerFrontingIt(t *testing.T) {
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "conformance")
	p := aws.NewProvider(aws.Options{Region: "us-east-1"}, nil, awssdk.Config{Region: "us-east-1"}, defaultNamespace)

	verdict, err := conformance.CheckContainerNeeds(t, p, "cloudflare", provider.Options{"cache": true}, edge.AllNeeds())
	if err != nil {
		t.Fatalf("the need check = %v, want it to pass: the worker dispatches a container it fronts as it does a function", err)
	}
	if !slices.Equal(verdict.InEffect, edge.AllNeeds()) {
		t.Errorf("needs in effect = %v, want every need", verdict.InEffect)
	}
	if dispatched := conformance.ListDispatchedComputes(t, p, "cloudflare", provider.Options{"cache": true}); !slices.Contains(dispatched, provider.ComputeContainer) {
		t.Errorf("computes cloudflare dispatches with cache = %v, want %s", dispatched, provider.ComputeContainer)
	}
}

func TestNoFrontOnAWSDispatchesANextContainer(t *testing.T) {
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "conformance")
	p := aws.NewProvider(aws.Options{Region: "us-east-1"}, nil, awssdk.Config{Region: "us-east-1"}, defaultNamespace)

	for _, front := range []edge.Kind{"cloudflare", cloudfront.Kind} {
		if dispatched := conformance.ListDispatchedComputes(t, p, front, nil); slices.Contains(dispatched, provider.ComputeContainer) {
			t.Errorf("computes %q dispatches = %v, want no %s: a container's requests are forwarded to its load balancer", front, dispatched, provider.ComputeContainer)
		}
	}
}
