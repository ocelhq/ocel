package cloudfront

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudfront"
	cftypes "github.com/aws/aws-sdk-go-v2/service/cloudfront/types"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func pollStoreQuickly(t *testing.T, every, within time.Duration) {
	t.Helper()
	heldEvery, heldWithin := keyValueStoreEvery, keyValueStoreWithin
	keyValueStoreEvery, keyValueStoreWithin = every, within
	t.Cleanup(func() { keyValueStoreEvery, keyValueStoreWithin = heldEvery, heldWithin })
}

func TestAFreshKeyValueStoreIsWaitedOn(t *testing.T) {
	pollStoreQuickly(t, time.Millisecond, time.Minute)

	w := newWorld()
	w.front.storeProvisions = 3

	arn, err := ensureKeyValueStore(context.Background(), w.clients(), edge.ClassProduction)
	if err != nil {
		t.Fatalf("ensureKeyValueStore: %v", err)
	}

	if arn == "" {
		t.Error("ensureKeyValueStore returned no ARN, want the created store's")
	}
	if got := w.front.count("DescribeKeyValueStore"); got != 4 {
		t.Errorf("DescribeKeyValueStore calls = %d, want 4: the miss, then one per poll until READY", got)
	}
}

func TestAReadyKeyValueStoreIsNotWaitedOn(t *testing.T) {
	pollStoreQuickly(t, time.Hour, time.Hour)

	w := newWorld()
	c := w.clients()
	if _, err := ensureKeyValueStore(context.Background(), c, edge.ClassProduction); err != nil {
		t.Fatalf("ensureKeyValueStore: %v", err)
	}
	before := w.front.count("DescribeKeyValueStore")

	if _, err := ensureKeyValueStore(context.Background(), c, edge.ClassProduction); err != nil {
		t.Fatalf("ensureKeyValueStore again: %v", err)
	}

	if got := w.front.count("DescribeKeyValueStore") - before; got != 1 {
		t.Errorf("DescribeKeyValueStore calls = %d, want 1: a store that reads READY is not polled", got)
	}
}

func TestAKeyValueStoreThatNeverProvisionsIsGivenUpOn(t *testing.T) {
	pollStoreQuickly(t, time.Millisecond, 5*time.Millisecond)

	w := newWorld()
	w.front.storeProvisions = 1000

	_, err := ensureKeyValueStore(context.Background(), w.clients(), edge.ClassProduction)

	if err == nil {
		t.Fatal("ensureKeyValueStore error = nil, want it to give up rather than poll forever")
	}
	if !strings.Contains(err.Error(), "finish provisioning") {
		t.Errorf("err = %q, want it to say what it waited for", err)
	}
	if !strings.Contains(err.Error(), "run the same command again") {
		t.Errorf("err = %q, want it to say what to do next", err)
	}
}

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
