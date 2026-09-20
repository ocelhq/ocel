package host

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// The worked example in AWS's "Signature Calculations: Using Query Parameters"
// documentation, whose expected signature the docs publish.
func TestQueryPresigningMatchesThePublishedExample(t *testing.T) {
	t.Parallel()

	at, err := url.Parse("https://examplebucket.s3.amazonaws.com/test.txt")
	if err != nil {
		t.Fatal(err)
	}
	signed := presignURL(http.MethodGet, at, credential{
		AccessKeyID: "AKIAIOSFODNN7EXAMPLE",
		SecretKey:   "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
		Region:      "us-east-1",
	}, 86400*time.Second, time.Date(2013, 5, 24, 0, 0, 0, 0, time.UTC))

	const want = "aeeed9bbccd4d02ee5c0109b86d86835f995330da4c265957d157751f604d404"
	if !strings.HasSuffix(signed, "X-Amz-Signature="+want) {
		t.Fatalf("presigned url = %q, want it to end in the published signature %s", signed, want)
	}
	for _, held := range []string{
		"X-Amz-Algorithm=AWS4-HMAC-SHA256",
		"X-Amz-Credential=AKIAIOSFODNN7EXAMPLE%2F20130524%2Fus-east-1%2Fs3%2Faws4_request",
		"X-Amz-Date=20130524T000000Z",
		"X-Amz-Expires=86400",
		"X-Amz-SignedHeaders=host",
	} {
		if !strings.Contains(signed, held) {
			t.Errorf("presigned url = %q, want it to carry %s", signed, held)
		}
	}
}

// The worked example in AWS's "Signature Calculations for the Authorization
// Header" documentation, whose expected signature the docs publish.
func TestSigningMatchesThePublishedExample(t *testing.T) {
	t.Parallel()

	req, err := http.NewRequest(http.MethodGet, "https://examplebucket.s3.amazonaws.com/test.txt", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Range", "bytes=0-9")

	const emptyPayload = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	signRequest(req, credential{
		AccessKeyID: "AKIAIOSFODNN7EXAMPLE",
		SecretKey:   "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
		Region:      "us-east-1",
	}, emptyPayload, time.Date(2013, 5, 24, 0, 0, 0, 0, time.UTC))

	const want = "f0e8bdb87c964420e857bd35b5d6ed310bd44f0170aba48dd91039c6036bdb41"
	authorization := req.Header.Get("Authorization")
	if !strings.HasSuffix(authorization, "Signature="+want) {
		t.Fatalf("Authorization = %q, want it to end in the published signature %s", authorization, want)
	}
	if !strings.Contains(authorization, "Credential=AKIAIOSFODNN7EXAMPLE/20130524/us-east-1/s3/aws4_request") {
		t.Errorf("Authorization = %q, want the published credential scope", authorization)
	}
	if !strings.Contains(authorization, "SignedHeaders=host;range;x-amz-content-sha256;x-amz-date") {
		t.Errorf("Authorization = %q, want the published signed-header list", authorization)
	}
}

func TestAQueryOnlySubresourceIsSignedAsAnEmptyValue(t *testing.T) {
	t.Parallel()

	req, err := http.NewRequest(http.MethodPut, "http://127.0.0.1:9000/uploads?cors", nil)
	if err != nil {
		t.Fatal(err)
	}
	signRequest(req, credential{AccessKeyID: "a", SecretKey: "b", Region: "us-east-1"},
		"00", time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))

	if got := canonicalQuery(req.URL); got != "cors=" {
		t.Fatalf("canonicalQuery = %q, want %q: a subresource with no value still signs as one", got, "cors=")
	}
	if req.Header.Get("X-Amz-Date") != "20260102T030405Z" {
		t.Fatalf("X-Amz-Date = %q, want the instant the request was signed at", req.Header.Get("X-Amz-Date"))
	}
}
