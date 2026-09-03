package control

import (
	"context"
	"os"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sts"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/platform/aws/provider/bootstrap"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const Vendor providerkit.Vendor = "AWS"

const credentialHeading = "AWS credentials"

const credentialHint = "configure AWS credentials (set AWS_PROFILE, run `aws sso login`, or export access keys)"

type STSAPI interface {
	GetCallerIdentity(ctx context.Context, in *sts.GetCallerIdentityInput, optFns ...func(*sts.Options)) (*sts.GetCallerIdentityOutput, error)
}

type Credentials struct {
	STS     STSAPI
	Region  string
	Profile string
}

func CredentialsFor(cfg aws.Config) Credentials {
	return Credentials{
		STS:     sts.NewFromConfig(cfg),
		Region:  cfg.Region,
		Profile: os.Getenv("AWS_PROFILE"),
	}
}

func (c Credentials) Whoami(ctx context.Context) (providerkit.Identity, error) {
	out, err := c.STS.GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		return providerkit.Identity{}, providerkit.Refuse(providerkit.CodeDenied, "%s", credentialHint)
	}
	arn := aws.ToString(out.Arn)
	return providerkit.Identity{
		Provider:  Vendor,
		Account:   aws.ToString(out.Account),
		Principal: principalOf(arn),
		Location:  c.Region,
		Details:   details(c.Profile),
	}, nil
}

func (c Credentials) Permissions(tier providerkit.CredentialTier) (edge.CredentialDocument, error) {
	var (
		document string
		err      error
	)
	switch tier {
	case providerkit.TierBootstrap:
		document, err = bootstrap.BootstrapCredentialPermissions()
	case providerkit.TierDeploy:
		document, err = bootstrap.DeployCredentialPermissions()
	default:
		return edge.CredentialDocument{}, providerkit.Refuse(providerkit.CodeInvalid,
			"credential permissions are rendered for the bootstrap tier or the deploy tier; this request named neither")
	}
	if err != nil {
		return edge.CredentialDocument{}, err
	}
	return edge.CredentialDocument{Heading: credentialHeading, Document: document}, nil
}

func principalOf(arn string) string {
	if i := strings.LastIndex(arn, "/"); i >= 0 {
		return arn[i+1:]
	}
	if i := strings.LastIndex(arn, ":"); i >= 0 {
		return arn[i+1:]
	}
	return arn
}

func details(profile string) []providerkit.Detail {
	if profile == "" {
		return nil
	}
	return []providerkit.Detail{{Label: "profile", Value: profile}}
}

var _ providerkit.Credentials = Credentials{}
