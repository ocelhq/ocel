package bootstrap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	ssmtypes "github.com/aws/aws-sdk-go-v2/service/ssm/types"
)

const (
	// EdgeCredentialsParamName / EdgeCredentialsPreviewParamName are the SSM
	// SecureString parameters holding each substrate's edge reader access key.
	EdgeCredentialsParamName        = "/ocel/edge/credentials"
	EdgeCredentialsPreviewParamName = "/ocel/edge/credentials-preview"
)

// IAMAPI is the subset of the IAM client the edge-credential step needs.
type IAMAPI interface {
	CreateAccessKey(ctx context.Context, in *iam.CreateAccessKeyInput, optFns ...func(*iam.Options)) (*iam.CreateAccessKeyOutput, error)
}

// EdgeCredentials is the JSON payload stored in SSM: the long-lived access key
// the Cloudflare worker signs its direct ISR reads with.
type EdgeCredentials struct {
	AccessKeyID     string `json:"accessKeyId"`
	SecretAccessKey string `json:"secretAccessKey"`
}

func edgeUserName(class string) (string, error) {
	switch class {
	case ClassProduction:
		return EdgeUserName, nil
	case ClassPreview:
		return EdgePreviewUserName, nil
	default:
		return "", fmt.Errorf("edge credentials: unknown substrate class %q", class)
	}
}

func edgeParamName(class string) (string, error) {
	switch class {
	case ClassProduction:
		return EdgeCredentialsParamName, nil
	case ClassPreview:
		return EdgeCredentialsPreviewParamName, nil
	default:
		return "", fmt.Errorf("edge credentials: unknown substrate class %q", class)
	}
}

// ensureEdgeCredentials mints the edge reader's access key if the substrate has
// none yet, storing it as an SSM SecureString, and never overwrites an existing
// one. Existence of the SSM parameter is the sole signal that the key is already
// minted, so a redeploy reuses it. Access keys are created imperatively because
// CloudFormation cannot surface a secret access key without leaking it into
// template output — the same reason the Pulumi passphrase is minted this way. It
// reports whether it minted a new key.
//
// Credential rotation runbook (no automation): mint a second key with
// iam.CreateAccessKey for the same user, re-inject it into every deployed
// worker's bindings, then delete the first key. Staged this way, nothing is ever
// signed with a key that has already been revoked. IAM caps a user at two keys,
// which is exactly the overlap this needs.
func ensureEdgeCredentials(ctx context.Context, iamClient IAMAPI, ssmClient SSMAPI, class string) (created bool, err error) {
	paramName, err := edgeParamName(class)
	if err != nil {
		return false, err
	}
	userName, err := edgeUserName(class)
	if err != nil {
		return false, err
	}

	_, err = ssmClient.GetParameter(ctx, &ssm.GetParameterInput{
		Name:           aws.String(paramName),
		WithDecryption: aws.Bool(true),
	})
	if err == nil {
		return false, nil
	}
	var notFound *ssmtypes.ParameterNotFound
	if !errors.As(err, &notFound) {
		return false, fmt.Errorf("read edge credentials parameter: %w", err)
	}

	out, err := iamClient.CreateAccessKey(ctx, &iam.CreateAccessKeyInput{
		UserName: aws.String(userName),
	})
	if err != nil {
		return false, fmt.Errorf("mint edge access key for %s: %w", userName, err)
	}
	payload, err := json.Marshal(EdgeCredentials{
		AccessKeyID:     aws.ToString(out.AccessKey.AccessKeyId),
		SecretAccessKey: aws.ToString(out.AccessKey.SecretAccessKey),
	})
	if err != nil {
		return false, fmt.Errorf("marshal edge credentials: %w", err)
	}
	if _, err := ssmClient.PutParameter(ctx, &ssm.PutParameterInput{
		Name:      aws.String(paramName),
		Value:     aws.String(string(payload)),
		Type:      ssmtypes.ParameterTypeSecureString,
		Overwrite: aws.Bool(false),
	}); err != nil {
		return false, fmt.Errorf("write edge credentials parameter: %w", err)
	}
	return true, nil
}

// ReadEdgeCredentials returns the substrate's stored edge credentials, decrypted,
// for the deploy path to inject into the worker's signed-read bindings.
func ReadEdgeCredentials(ctx context.Context, ssmClient SSMAPI, class string) (EdgeCredentials, error) {
	paramName, err := edgeParamName(class)
	if err != nil {
		return EdgeCredentials{}, err
	}
	out, err := ssmClient.GetParameter(ctx, &ssm.GetParameterInput{
		Name:           aws.String(paramName),
		WithDecryption: aws.Bool(true),
	})
	if err != nil {
		return EdgeCredentials{}, fmt.Errorf("read edge credentials parameter: %w", err)
	}
	var creds EdgeCredentials
	if err := json.Unmarshal([]byte(aws.ToString(out.Parameter.Value)), &creds); err != nil {
		return EdgeCredentials{}, fmt.Errorf("parse edge credentials: %w", err)
	}
	return creds, nil
}
