package s3

import (
	"context"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/durationpb"

	bucketv1 "github.com/ocelhq/ocel/pkg/proto/app/bucket/v1"
)

func TestASignedUrlOutlivesNothingLongerThanSigV4Allows(t *testing.T) {
	t.Parallel()

	h := newHarness(t, nil)
	resp, err := h.svc.Sign(context.Background(), &bucketv1.SignRequest{
		Bucket: "store", Key: "a.png",
		Operation: bucketv1.SignedOperation_SIGNED_OPERATION_GET,
		ExpiresIn: durationpb.New(30 * 24 * time.Hour),
	})
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	signed, err := url.Parse(resp.GetTarget().GetUrl())
	if err != nil {
		t.Fatal(err)
	}
	seconds, err := strconv.Atoi(signed.Query().Get("X-Amz-Expires"))
	if err != nil {
		t.Fatalf("the signed url has %q as its expiry", signed.Query().Get("X-Amz-Expires"))
	}
	if want := int((7 * 24 * time.Hour).Seconds()); seconds != want {
		t.Errorf("a url asked to last 30 days was signed for %d seconds, want %d: sigv4 refuses anything longer and the store answers 403 to every one of them",
			seconds, want)
	}
}

func TestAStoreWhoseClockDisagreesSaysSoInSoManyWords(t *testing.T) {
	t.Parallel()

	refusal := storeError("head a.png", &apiError{code: "RequestTimeTooSkewed"})
	if refusal == nil {
		t.Fatal("a store that refused the signature came back as success")
	}
	for _, said := range []string{"clock", "head a.png"} {
		if !strings.Contains(refusal.Error(), said) {
			t.Errorf("the error never says %q:\n%v", said, refusal)
		}
	}
}
