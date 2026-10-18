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

func TestAPartIsSignedForNoLongerThanTheSweepLeavesItsUploadOpen(t *testing.T) {
	t.Parallel()

	h := newHarness(t, func(cfg *Config) { cfg.SweepUploads = true })
	opened, err := h.svc.CreateMultipart(context.Background(), &bucketv1.CreateMultipartRequest{Bucket: "store", Key: "big.bin"})
	if err != nil {
		t.Fatalf("CreateMultipart: %v", err)
	}
	h.now = h.now.Add(orphanAge - time.Hour)

	resp, err := h.svc.SignParts(context.Background(), &bucketv1.SignPartsRequest{
		Bucket: "store", Key: "big.bin", UploadId: opened.GetUploadId(), PartNumbers: []int32{1},
		ExpiresIn: durationpb.New(maxPresignTTL),
	})
	if err != nil {
		t.Fatalf("SignParts: %v", err)
	}
	signed, err := url.Parse(resp.GetParts()[0].GetUrl())
	if err != nil {
		t.Fatal(err)
	}
	seconds, err := strconv.Atoi(signed.Query().Get("X-Amz-Expires"))
	if err != nil {
		t.Fatalf("the signed part has %q as its expiry", signed.Query().Get("X-Amz-Expires"))
	}
	if limit := int(time.Hour.Seconds()); seconds > limit {
		t.Errorf("a part of an upload the sweep aborts in an hour was signed for %d seconds, want at most %d: the part fails mid-transfer", seconds, limit)
	}

	h.now = h.now.Add(2 * time.Hour)
	if _, err := h.svc.SignParts(context.Background(), &bucketv1.SignPartsRequest{
		Bucket: "store", Key: "big.bin", UploadId: opened.GetUploadId(), PartNumbers: []int32{2},
	}); err == nil {
		t.Error("SignParts signed a part of an upload the sweep may already have aborted")
	}
}

func TestAPartOfAnUploadNoSweepAbortsIsSignedForAsLongAsAsked(t *testing.T) {
	t.Parallel()

	h := newHarness(t, nil)
	resp, err := h.svc.SignParts(context.Background(), &bucketv1.SignPartsRequest{
		Bucket: "store", Key: "big.bin", UploadId: "upload-from-elsewhere", PartNumbers: []int32{1},
		ExpiresIn: durationpb.New(maxPresignTTL),
	})
	if err != nil {
		t.Fatalf("SignParts on a store whose own lifecycle aborts uploads: %v", err)
	}
	signed, err := url.Parse(resp.GetParts()[0].GetUrl())
	if err != nil {
		t.Fatal(err)
	}
	if got, want := signed.Query().Get("X-Amz-Expires"), strconv.Itoa(int(maxPresignTTL.Seconds())); got != want {
		t.Errorf("a part was signed for %s seconds, want the %s asked for", got, want)
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
