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

	// EdgeValuesParamName / EdgeValuesPreviewParamName are the SSM SecureString
	// parameters holding each substrate's edge bootstrap outputs.
	EdgeValuesParamName        = "/ocel/edge/values"
	EdgeValuesPreviewParamName = "/ocel/edge/values-preview"
)

// IAMAPI is the subset of the IAM client the edge-credential step needs.
type IAMAPI interface {
	ListAccessKeys(ctx context.Context, in *iam.ListAccessKeysInput, optFns ...func(*iam.Options)) (*iam.ListAccessKeysOutput, error)
	CreateAccessKey(ctx context.Context, in *iam.CreateAccessKeyInput, optFns ...func(*iam.Options)) (*iam.CreateAccessKeyOutput, error)
}

// EdgeCredentials is the JSON payload stored in SSM: the long-lived access key
// the Cloudflare worker signs its direct ISR reads with.
type EdgeCredentials struct {
	AccessKeyID     string `json:"accessKeyId"`
	SecretAccessKey string `json:"secretAccessKey"`
}

// edgeNames are the identities the edge step addresses for one substrate class:
// its IAM user and the two SSM parameters holding its credentials and values.
type edgeNames struct {
	user             string
	credentialsParam string
	valuesParam      string
}

var edgeNamesByClass = map[string]edgeNames{
	ClassProduction: {EdgeUserName, EdgeCredentialsParamName, EdgeValuesParamName},
	ClassPreview:    {EdgePreviewUserName, EdgeCredentialsPreviewParamName, EdgeValuesPreviewParamName},
}

func edgeNamesFor(class string) (edgeNames, error) {
	names, ok := edgeNamesByClass[class]
	if !ok {
		return edgeNames{}, fmt.Errorf("edge: unknown substrate class %q", class)
	}
	return names, nil
}

// writeEdgeValues stores the edge's own bootstrap outputs so the deploy path can
// hand them back verbatim. They are opaque to the provider — stored and read as
// one blob, never keyed into — and are held as a SecureString because an edge is
// free to put a secret among them. Unlike the minted access key, they are the
// edge's current state rather than something that must survive re-minting, so a
// re-run overwrites them.
func writeEdgeValues(ctx context.Context, ssmClient SSMAPI, class string, values map[string]string) error {
	names, err := edgeNamesFor(class)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(values)
	if err != nil {
		return fmt.Errorf("marshal edge values: %w", err)
	}
	if _, err := ssmClient.PutParameter(ctx, &ssm.PutParameterInput{
		Name:      aws.String(names.valuesParam),
		Value:     aws.String(string(payload)),
		Type:      ssmtypes.ParameterTypeSecureString,
		Overwrite: aws.Bool(true),
	}); err != nil {
		return fmt.Errorf("write edge values parameter: %w", err)
	}
	return nil
}

// ReadEdgeValues returns the substrate's stored edge bootstrap outputs, decrypted,
// for the deploy path to hand back to the edge. A substrate whose edge stored
// none reads as empty rather than as a failure.
func ReadEdgeValues(ctx context.Context, ssmClient SSMAPI, class string) (map[string]string, error) {
	names, err := edgeNamesFor(class)
	if err != nil {
		return nil, err
	}
	out, err := ssmClient.GetParameter(ctx, &ssm.GetParameterInput{
		Name:           aws.String(names.valuesParam),
		WithDecryption: aws.Bool(true),
	})
	if err != nil {
		var notFound *ssmtypes.ParameterNotFound
		if errors.As(err, &notFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("read edge values parameter: %w", err)
	}
	var values map[string]string
	if err := json.Unmarshal([]byte(aws.ToString(out.Parameter.Value)), &values); err != nil {
		return nil, fmt.Errorf("parse edge values: %w", err)
	}
	return values, nil
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
	names, err := edgeNamesFor(class)
	if err != nil {
		return false, err
	}
	paramName, userName := names.credentialsParam, names.user

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

	// Guard against the dangling-key trap: if a prior run minted a key but failed
	// before storing it in SSM, GetParameter still reads ParameterNotFound and we
	// would mint again. Two such failures hit the AWS 2-key cap and wedge every
	// later bootstrap at CreateAccessKey with an opaque LimitExceeded. Fail early
	// with a diagnostic pointing at the rotation runbook instead.
	keys, err := iamClient.ListAccessKeys(ctx, &iam.ListAccessKeysInput{
		UserName: aws.String(userName),
	})
	if err != nil {
		return false, fmt.Errorf("list edge access keys for %s: %w", userName, err)
	}
	if len(keys.AccessKeyMetadata) >= 2 {
		return false, fmt.Errorf(
			"edge reader %s already has %d access keys but none is stored in %s: "+
				"a prior mint likely failed before its PutParameter; delete a stale "+
				"key with iam.DeleteAccessKey, then re-run bootstrap",
			userName, len(keys.AccessKeyMetadata), paramName,
		)
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
	names, err := edgeNamesFor(class)
	if err != nil {
		return EdgeCredentials{}, err
	}
	out, err := ssmClient.GetParameter(ctx, &ssm.GetParameterInput{
		Name:           aws.String(names.credentialsParam),
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
