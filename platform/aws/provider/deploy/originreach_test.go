package deploy

import (
	"reflect"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
)

func TestTheEdgeReachesAnAWSOriginBySigningForLambdaAndReadsEntriesFromTheEdgeBucket(t *testing.T) {
	t.Parallel()

	got := OriginReach(OriginAccess{
		Region:            "eu-west-2",
		EdgeBucket:        "ocel-prod-edge-k3x9q2",
		AccessKeyID:       "AKIA",
		SecretAccessKey:   "secret",
		ImageOptimizerURL: "https://optimizer.example",
	})

	want := edge.OriginReach{
		Reach:    edge.Reach{Kind: edge.ReachSigV4, Service: "lambda", Region: "eu-west-2", AccessKeyID: "AKIA", SecretAccessKey: "secret"},
		Store:    &edge.OriginStore{URL: "https://ocel-prod-edge-k3x9q2.s3.eu-west-2.amazonaws.com", Region: "eu-west-2", HoldsEntries: true, AccessKeyID: "AKIA", SecretAccessKey: "secret"},
		Throttle: edge.Throttle{Status: 429, Header: "x-amzn-errortype"},
		Images:   &edge.OriginImages{OptimizerURL: "https://optimizer.example", MaxRequestBytes: MaxImageRequestBytes},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("OriginReach() = %+v, want %+v", got, want)
	}
	if err := got.RefuseIncomplete(); err != nil {
		t.Errorf("RefuseIncomplete() = %v, want the AWS reach complete", err)
	}
}

func TestAnAWSOriginWithNoEdgeBucketOrOptimizerNamesNoStoreOrImagesToTheEdge(t *testing.T) {
	t.Parallel()

	got := OriginReach(OriginAccess{Region: "eu-west-2", AccessKeyID: "AKIA", SecretAccessKey: "secret"})
	if got.Store != nil || got.Images != nil {
		t.Errorf("OriginReach() = store %+v, images %+v, want neither: the bootstrap recorded no bucket or optimizer", got.Store, got.Images)
	}
}

func TestAnImageRequestTheEdgeSendsAnAWSOptimizerFitsALambdaInvokePayload(t *testing.T) {
	t.Parallel()

	if MaxImageRequestBytes >= 6<<20 {
		t.Errorf("MaxImageRequestBytes = %d, want under Lambda's 6 MiB invoke payload so the function URL's event fits around it", MaxImageRequestBytes)
	}
}
