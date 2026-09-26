package control

import (
	"context"
	"os"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sts"

	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	"github.com/ocelhq/ocel/pkg/providerkit/refusal"
	"github.com/ocelhq/ocel/platform/aws/provider/bootstrap"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const credentialHeading = "AWS credentials"

const credentialHint = "configure AWS credentials (set AWS_PROFILE, run `aws sso login`, or export access keys)"

type STSAPI interface {
	GetCallerIdentity(ctx context.Context, in *sts.GetCallerIdentityInput, optFns ...func(*sts.Options)) (*sts.GetCallerIdentityOutput, error)
}

type Credentials struct {
	STS     STSAPI
	Region  string
	Profile string

	Namespace bootstrap.Namespace
}

func CredentialsFor(cfg aws.Config, ns bootstrap.Namespace) Credentials {
	return Credentials{
		STS:     sts.NewFromConfig(cfg),
		Region:  cfg.Region,
		Profile: os.Getenv("AWS_PROFILE"),

		Namespace: ns,
	}
}

func (c Credentials) Whoami(ctx context.Context) (provider.Identity, error) {
	out, err := c.STS.GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		return provider.Identity{}, refusal.Refuse(refusal.CodeDenied, "%s: %v", credentialHint, err)
	}
	arn := aws.ToString(out.Arn)
	return provider.Identity{
		Account:   aws.ToString(out.Account),
		Principal: principalOf(arn),
		Location:  c.Region,
		Details:   details(c.Profile),
	}, nil
}

func (c Credentials) Permissions(tier provider.CredentialTier) (edge.CredentialDocument, error) {
	var (
		document string
		err      error
	)
	switch tier {
	case provider.TierBootstrap:
		document, err = bootstrap.BootstrapCredentialPermissions(c.Namespace)
	case provider.TierDeploy:
		document, err = bootstrap.DeployCredentialPermissions(c.Namespace)
	default:
		return edge.CredentialDocument{}, refusal.Refuse(refusal.CodeInvalid,
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

func details(profile string) []provider.Detail {
	if profile == "" {
		return nil
	}
	return []provider.Detail{{Label: "profile", Value: profile}}
}

var _ provider.Credentials = Credentials{}
