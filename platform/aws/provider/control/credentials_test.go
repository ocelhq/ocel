package control

import (
	"context"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sts"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
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

	principal, err := creds.Whoami(context.Background())
	if err != nil {
		t.Fatalf("Whoami() = %v", err)
	}
	if principal.Account != "123456789012" || principal.Name != "deployer" {
		t.Errorf("Whoami() = %+v, want the account and the principal the ARN names", principal)
	}
	if principal.Location != "eu-west-1" {
		t.Errorf("Whoami().Location = %q, want the region this run acts in", principal.Location)
	}
	if len(principal.Details) != 0 {
		t.Errorf("Whoami().Details = %+v, want nothing beside a region already said and a profile never set", principal.Details)
	}
}

func TestWhoamiKeepsTheProfileAmongTheDetails(t *testing.T) {
	t.Parallel()

	creds := Credentials{
		STS:     callerIdentity{account: "123456789012", arn: "arn:aws:iam::123456789012:role/deploy/session"},
		Region:  "us-east-1",
		Profile: "acme",
	}

	principal, err := creds.Whoami(context.Background())
	if err != nil {
		t.Fatalf("Whoami() = %v", err)
	}
	if len(principal.Details) != 1 || principal.Details[0].Label != "profile" || principal.Details[0].Value != "acme" {
		t.Errorf("Whoami().Details = %+v, want the profile the credentials were read from", principal.Details)
	}
}

func TestPermissionsRenderTheTierTheyAreAskedForAndNameIt(t *testing.T) {
	t.Parallel()

	credentials := Credentials{Namespace: "acme"}
	for purpose, tier := range map[edge.CredentialPurpose]environment.Tier{
		edge.PurposeBootstrap: environment.TierPreview,
		edge.PurposeDeploy:    environment.TierProduction,
	} {
		document, err := credentials.Permissions(purpose, tier)
		if err != nil {
			t.Fatalf("Permissions(%s, %s) = %v", purpose, tier, err)
		}
		if !strings.Contains(document.Heading, string(tier)) {
			t.Errorf("Permissions(%s, %s).Heading = %q, want it to name the tier", purpose, tier, document.Heading)
		}
		if want := "alias/acme-variables-" + string(tier); !strings.Contains(document.Document, want) {
			t.Errorf("Permissions(%s, %s) does not fence the variables key to %s", purpose, tier, want)
		}
		if other := "alias/acme-variables-" + string(tier.Sibling()); strings.Contains(document.Document, other) {
			t.Errorf("Permissions(%s, %s) names %s, the other tier's variables key", purpose, tier, other)
		}
	}
}

func TestPermissionsRefuseATierNoBootstrapServes(t *testing.T) {
	t.Parallel()

	if _, err := (Credentials{Namespace: "acme"}).Permissions(edge.PurposeDeploy, "staging"); err == nil {
		t.Error("Permissions(deploy, staging) rendered a document, want a refusal")
	}
}
