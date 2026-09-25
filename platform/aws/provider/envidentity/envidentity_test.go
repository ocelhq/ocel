package envidentity_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"

	"github.com/ocelhq/ocel/platform/aws/provider/envidentity"
)

func TestTheSignedCallerIdentityRequestIsTheOneInfisicalReplaysToRegionalSTS(t *testing.T) {
	signer := envidentity.Signer{
		Config: aws.Config{Region: "eu-west-2", Credentials: credentials.NewStaticCredentialsProvider("AKID", "secret", "session-token")},
		Now:    func() time.Time { return time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC) },
	}
	signed, err := signer.SignCallerIdentity(context.Background())
	if err != nil {
		t.Fatalf("SignCallerIdentity() = %v", err)
	}
	if signed.Method != http.MethodPost || signed.URL != "https://sts.eu-west-2.amazonaws.com/" {
		t.Fatalf("signed %s %s, want a POST to the regional STS endpoint", signed.Method, signed.URL)
	}
	if string(signed.Body) != "Action=GetCallerIdentity&Version=2011-06-15" {
		t.Fatalf("body = %q", signed.Body)
	}
	authorization := signed.Header.Get("Authorization")
	if !strings.HasPrefix(authorization, "AWS4-HMAC-SHA256 Credential=AKID/20260925/eu-west-2/sts/aws4_request") {
		t.Fatalf("Authorization = %q, want it scoped to sts in the region Infisical reads from it", authorization)
	}
	if signed.Header.Get("X-Amz-Date") != "20260925T120000Z" || signed.Header.Get("X-Amz-Security-Token") != "session-token" {
		t.Fatalf("headers = %v", signed.Header)
	}
}

func TestSigningWithNoRegionIsRefused(t *testing.T) {
	signer := envidentity.Signer{Config: aws.Config{Credentials: credentials.NewStaticCredentialsProvider("AKID", "secret", "")}}
	if _, err := signer.SignCallerIdentity(context.Background()); err == nil || !strings.Contains(err.Error(), "region") {
		t.Fatalf("SignCallerIdentity() with no region = %v, want it refused", err)
	}
}
