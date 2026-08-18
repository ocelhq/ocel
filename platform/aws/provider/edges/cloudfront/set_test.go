package cloudfront

import (
	"context"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudfront"
	cftypes "github.com/aws/aws-sdk-go-v2/service/cloudfront/types"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func TestFindingAPolicyReadsEveryPage(t *testing.T) {
	t.Parallel()

	w := newWorld()
	bootstrapped(t, w)
	w.front.cachePolicies["cache-0"] = "someone-elses-policy"
	w.front.cachePolicyPageSize = 1

	id, err := findCachePolicy(context.Background(), w.clients(), cachePolicyName(edge.ClassProduction))
	if err != nil {
		t.Fatalf("findCachePolicy: %v", err)
	}

	if id == "" {
		t.Error("findCachePolicy found nothing, want the policy bootstrap made on a later page")
	}
	if got := w.front.count("ListCachePolicies"); got < 2 {
		t.Errorf("ListCachePolicies calls = %d, want at least 2: one call per marker CloudFront hands back", got)
	}
}

type endlessPages struct{ *fakeCloudFront }

func (endlessPages) ListCachePolicies(context.Context, *cloudfront.ListCachePoliciesInput, ...func(*cloudfront.Options)) (*cloudfront.ListCachePoliciesOutput, error) {
	return &cloudfront.ListCachePoliciesOutput{CachePolicyList: &cftypes.CachePolicyList{
		NextMarker: aws.String("more"),
	}}, nil
}

func TestAListingThatNeverEndsIsGivenUpOn(t *testing.T) {
	t.Parallel()

	w := newWorld()
	c := w.clients()
	c.CloudFront = endlessPages{fakeCloudFront: w.front}

	_, err := findCachePolicy(context.Background(), c, cachePolicyName(edge.ClassProduction))

	if err == nil {
		t.Fatal("findCachePolicy error = nil, want it to stop rather than page forever")
	}
	if !strings.Contains(err.Error(), "run the same command again") {
		t.Errorf("err = %q, want it to say what to do next", err)
	}
}
