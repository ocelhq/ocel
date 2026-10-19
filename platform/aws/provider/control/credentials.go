package control

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
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
	"github.com/ocelhq/ocel/platform/aws/provider/account"
	"github.com/ocelhq/ocel/platform/aws/provider/bastion"
	"github.com/ocelhq/ocel/platform/aws/provider/certs"
	"github.com/ocelhq/ocel/platform/aws/provider/component"
	"github.com/ocelhq/ocel/platform/aws/provider/compute/container"
	"github.com/ocelhq/ocel/platform/aws/provider/compute/function"
	"github.com/ocelhq/ocel/platform/aws/provider/compute/worker"
	"github.com/ocelhq/ocel/platform/aws/provider/edges/alb"
	"github.com/ocelhq/ocel/platform/aws/provider/edges/apigateway"
	"github.com/ocelhq/ocel/platform/aws/provider/edges/cloudfront"
	"github.com/ocelhq/ocel/platform/aws/provider/iam"
	"github.com/ocelhq/ocel/platform/aws/provider/registry"
	"github.com/ocelhq/ocel/platform/aws/provider/resources"
	tiercloudfront "github.com/ocelhq/ocel/platform/aws/provider/tier/cloudfront"
	"github.com/ocelhq/ocel/platform/aws/provider/tier/containers"
	"github.com/ocelhq/ocel/platform/aws/provider/tier/envsourcesync"
	"github.com/ocelhq/ocel/platform/aws/provider/tier/functionnetwork"
	"github.com/ocelhq/ocel/platform/aws/provider/tier/notfoundapi"
	"github.com/ocelhq/ocel/platform/aws/provider/tier/taginvalidator"
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

	Namespace    account.Namespace
	VariablesKey string
}

func CredentialsFor(cfg aws.Config, ns account.Namespace, variablesKey string) Credentials {
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
		Vendor:   groupVendor,
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
		document, err = BootstrapCredentialPermissions(c.Namespace, tier, c.VariablesKey)
	case edge.PurposeDeploy:
		document, err = DeployCredentialPermissions(c.Namespace, tier, c.VariablesKey)
	default:
		return edge.CredentialDocument{}, refusal.Refuse(refusal.CodeInvalid,
			"credential permissions are rendered for bootstrap or deploy credentials; this request named neither")
	}
	if err != nil {
		return edge.CredentialDocument{}, err
	}
	return edge.CredentialDocument{Heading: fmt.Sprintf("%s for the %s tier, one managed policy per entry", credentialHeading, tier), Document: document}, nil
}

func deployStatements(s iam.Scope) []iam.Statement {
	statements := slices.Concat(
		account.AccessCredential(s),
		account.RuntimeLayerCredential(s),
		component.NetworkCredential(s),
		function.Credential(s),
		container.Credential(s),
		worker.Credential(s),
		bastion.Credential(s),
		registry.Credential(s),
		certs.Credential(s),
		functionnetwork.Credential(s),
		containers.Credential(s),
		envsourcesync.Credential(s),
		taginvalidator.Credential(s),
		tiercloudfront.Credential(s),
		notfoundapi.Credential(s),
		cloudfront.Credential(s),
		apigateway.Credential(s),
		alb.Credential(s),
	)
	for _, declaring := range resources.Registry(resources.Clients{}).Components() {
		statements = append(statements, declaring.Credential(s)...)
	}
	return statements
}

func bootstrapStatements(s iam.Scope) []iam.Statement {
	return slices.Concat(
		deployStatements(s),
		account.ProvisionCredential(s),
		account.EdgePrincipalCredential(s),
		account.RuntimeLayerReclaimCredential(s),
		tiercloudfront.ReclaimCredential(s),
		notfoundapi.ReclaimCredential(s),
	)
}

func refuseUnservedTier(tier environment.Tier) error {
	if tier == environment.TierProduction || tier == environment.TierPreview {
		return nil
	}
	return refusal.Refuse(refusal.CodeInvalid, "credential permissions are rendered for the production or preview tier, not %q", tier)
}

func scopeOf(ns account.Namespace, tier environment.Tier, broughtKey string) iam.Scope {
	s := ns.Scope(tier)
	s.BroughtVariablesKey = broughtKey
	return s
}

func DeployCredentialPermissions(ns account.Namespace, tier environment.Tier, broughtKey string) (string, error) {
	if err := refuseUnservedTier(tier); err != nil {
		return "", err
	}
	return credentialPolicies(ns, "deploy", tier, deployStatements(scopeOf(ns, tier, broughtKey)))
}

func BootstrapCredentialPermissions(ns account.Namespace, tier environment.Tier, broughtKey string) (string, error) {
	if err := refuseUnservedTier(tier); err != nil {
		return "", err
	}
	return credentialPolicies(ns, "bootstrap", tier, bootstrapStatements(scopeOf(ns, tier, broughtKey)))
}

func credentialPolicies(ns account.Namespace, purpose string, tier environment.Tier, grants []iam.Statement) (string, error) {
	policies, err := iam.Split(purpose, func(i int) string { return ns.PolicyName(fmt.Sprintf("%s-%s-%d", purpose, tier, i)) }, grants)
	if err != nil {
		return "", err
	}
	return iam.RenderManagedPolicies(purpose, policies)
}

func refuseCallerIdentity(err error) error {
	var apiErr smithy.APIError
	hasAPIError := errors.As(err, &apiErr)
	var invalidToken *ssocreds.InvalidTokenError
	var unsent *smithyhttp.RequestSendError
	var refused refusal.Option
	switch {
	case errors.As(err, &refused):
		return refused
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
