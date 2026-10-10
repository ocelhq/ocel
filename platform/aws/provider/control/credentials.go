package control

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials/ssocreds"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/aws/smithy-go"
	smithyhttp "github.com/aws/smithy-go/transport/http"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/aws/provider/bootstrap"
)

const credentialHeading = "AWS credentials"

const credentialHint = "configure AWS credentials (set AWS_PROFILE, run `aws sso login`, or export access keys)"

const regionHint = "set the region in the provider options, in AWS_REGION, or as the profile's region under a [profile <name>] header"

const expiredHint = "the AWS session has expired: run `aws sso login`, or refresh the temporary credentials"

const assumeRoleHint = "the profile's role could not be assumed: let the source principal call sts:AssumeRole on the role, and the role's trust policy admit it"

const unreachableHint = "the AWS STS endpoint could not be reached: check the network and any proxy"

type STSAPI interface {
	GetCallerIdentity(ctx context.Context, in *sts.GetCallerIdentityInput, optFns ...func(*sts.Options)) (*sts.GetCallerIdentityOutput, error)
}

type Credentials struct {
	STS     STSAPI
	Region  string
	Profile string

	Namespace    bootstrap.Namespace
	VariablesKey string
}

func CredentialsFor(cfg aws.Config, ns bootstrap.Namespace, variablesKey string) Credentials {
	return Credentials{
		STS:     sts.NewFromConfig(cfg),
		Region:  cfg.Region,
		Profile: os.Getenv("AWS_PROFILE"),

		Namespace:    ns,
		VariablesKey: variablesKey,
	}
}

func (c Credentials) Whoami(ctx context.Context) (provider.Principal, error) {
	out, err := c.STS.GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		return provider.Principal{}, refuseCallerIdentity(err)
	}
	arn := aws.ToString(out.Arn)
	return provider.Principal{
		Account:  aws.ToString(out.Account),
		Name:     principalOf(arn),
		Location: c.Region,
		Details:  details(c.Profile),
	}, nil
}

func (c Credentials) Permissions(purpose edge.CredentialPurpose, tier environment.Tier) (edge.CredentialDocument, error) {
	var (
		document string
		err      error
	)
	switch purpose {
	case edge.PurposeBootstrap:
		document, err = bootstrap.BootstrapCredentialPermissions(c.Namespace, tier, c.VariablesKey)
	case edge.PurposeDeploy:
		document, err = bootstrap.DeployCredentialPermissions(c.Namespace, tier, c.VariablesKey)
	default:
		return edge.CredentialDocument{}, refusal.Refuse(refusal.CodeInvalid,
			"credential permissions are rendered for bootstrap or deploy credentials; this request named neither")
	}
	if err != nil {
		return edge.CredentialDocument{}, err
	}
	return edge.CredentialDocument{Heading: fmt.Sprintf("%s for the %s tier", credentialHeading, tier), Document: document}, nil
}

func refuseCallerIdentity(err error) error {
	var apiErr smithy.APIError
	hasAPIError := errors.As(err, &apiErr)
	var invalidToken *ssocreds.InvalidTokenError
	var unsent *smithyhttp.RequestSendError
	switch {
	case strings.Contains(err.Error(), "Missing Region"):
		return refusal.Refuse(refusal.CodeInvalid, "%s: %v", regionHint, err)
	case errors.As(err, &invalidToken),
		hasAPIError && (apiErr.ErrorCode() == "ExpiredToken" || apiErr.ErrorCode() == "ExpiredTokenException"):
		return refusal.Refuse(refusal.CodeDenied, "%s: %v", expiredHint, err)
	case hasAPIError && apiErr.ErrorCode() == "AccessDenied":
		return refusal.Refuse(refusal.CodeDenied, "%s: %v", assumeRoleHint, err)
	case errors.As(err, &unsent):
		return refusal.Refuse(refusal.CodeNotReady, "%s: %v", unreachableHint, err)
	default:
		return refusal.Refuse(refusal.CodeDenied, "%s: %v", credentialHint, err)
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

func details(profile string) []provider.PrincipalDetail {
	if profile == "" {
		return nil
	}
	return []provider.PrincipalDetail{{Label: "profile", Value: profile}}
}

var _ provider.Credentials = Credentials{}
