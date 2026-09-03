package control

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sts"
)

type callerIdentity struct {
	account string
	arn     string
}

func (c callerIdentity) GetCallerIdentity(context.Context, *sts.GetCallerIdentityInput, ...func(*sts.Options)) (*sts.GetCallerIdentityOutput, error) {
	return &sts.GetCallerIdentityOutput{Account: aws.String(c.account), Arn: aws.String(c.arn)}, nil
}

func TestWhoamiPlacesTheRegionBesideTheAccountRatherThanAmongTheDetails(t *testing.T) {
	t.Parallel()

	creds := Credentials{
		STS:    callerIdentity{account: "123456789012", arn: "arn:aws:iam::123456789012:user/deployer"},
		Region: "eu-west-1",
	}

	identity, err := creds.Whoami(context.Background())
	if err != nil {
		t.Fatalf("Whoami() = %v", err)
	}
	if identity.Account != "123456789012" || identity.Principal != "deployer" {
		t.Errorf("Whoami() = %+v, want the account and the principal the ARN names", identity)
	}
	if identity.Location != "eu-west-1" {
		t.Errorf("Whoami().Location = %q, want the region this run acts in", identity.Location)
	}
	if len(identity.Details) != 0 {
		t.Errorf("Whoami().Details = %+v, want nothing beside a region already said and a profile never set", identity.Details)
	}
}

func TestWhoamiKeepsTheProfileAmongTheDetails(t *testing.T) {
	t.Parallel()

	creds := Credentials{
		STS:     callerIdentity{account: "123456789012", arn: "arn:aws:iam::123456789012:role/deploy/session"},
		Region:  "us-east-1",
		Profile: "acme",
	}

	identity, err := creds.Whoami(context.Background())
	if err != nil {
		t.Fatalf("Whoami() = %v", err)
	}
	if len(identity.Details) != 1 || identity.Details[0].Label != "profile" || identity.Details[0].Value != "acme" {
		t.Errorf("Whoami().Details = %+v, want the profile the credentials were read from", identity.Details)
	}
}
