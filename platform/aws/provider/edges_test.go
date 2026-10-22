package aws

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sts"

	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy"
)

func TestAnAWSOriginIsItsAccountAndRegion(t *testing.T) {
	t.Parallel()

	p := NewProvider(Options{Region: "eu-west-1"}, nil, aws.Config{Region: "eu-west-1"}, defaultNamespace)
	p.identity.remember(struct{}{}, &sts.GetCallerIdentityOutput{Account: aws.String("111122223333")})

	origin, err := p.cloudflareOrigin(t.Context())
	if err != nil {
		t.Fatalf("cloudflareOrigin() = %v", err)
	}
	if want := (cloudflare.OriginScope{Vendor: "aws", ID: "111122223333/eu-west-1"}); origin != want {
		t.Errorf("cloudflareOrigin() = %v, want %v: one bootstrap is one account and region", origin, want)
	}
}
