package control

import (
	"context"
	"os"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sts"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/platform/aws/provider/bootstrap"
)

const Vendor providerkit.Vendor = "AWS"

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
		Details:   details(c.Region, c.Profile),
	}, nil
}

func (c Credentials) Policy(tier providerkit.CredentialTier) (string, error) {
	switch tier {
	case providerkit.TierBootstrap:
		return bootstrap.BootstrapCredentialPolicy()
	case providerkit.TierDeploy:
		return bootstrap.DeployCredentialPolicy()
	default:
		return "", providerkit.Refuse(providerkit.CodeInvalid,
			"a credential policy is rendered for the bootstrap tier or the deploy tier; this request named neither")
	}
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

func details(region, profile string) []providerkit.Detail {
	var out []providerkit.Detail
	for _, detail := range []providerkit.Detail{{Label: "region", Value: region}, {Label: "profile", Value: profile}} {
		if detail.Value != "" {
			out = append(out, detail)
		}
	}
	return out
}

var _ providerkit.Credentials = Credentials{}
