package aws

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
)

func TestAnAWSTargetProvesItsIdentityAsItsOwnRole(t *testing.T) {
	cfg := aws.Config{Region: "eu-west-2", Credentials: credentials.NewStaticCredentialsProvider("AKID", "secret", "")}
	prove := NewProvider(Options{Region: "eu-west-2"}, nil, cfg, defaultNamespace).Hooks().ProveIdentity
	if prove == nil {
		t.Fatal("the aws provider sets no ProveIdentity hook, so every Infisical env source with identity auth is refused on an aws target")
	}
	proof, err := prove(context.Background(), "identity-1")
	if err != nil || proof.SignedRequest == nil || proof.SignedRequest.URL != "https://sts.eu-west-2.amazonaws.com/" {
		t.Fatalf("ProveIdentity() = %+v, %v, want a request signed for the target's own region", proof, err)
	}
}
