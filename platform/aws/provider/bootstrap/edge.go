package bootstrap

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	ssmtypes "github.com/aws/aws-sdk-go-v2/service/ssm/types"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const (
	EdgeCredentialsParamName        = "/ocel/edge/credentials"
	EdgeCredentialsPreviewParamName = "/ocel/edge/credentials-preview"

	EdgeValuesParamName        = "/ocel/edge/values"
	EdgeValuesPreviewParamName = "/ocel/edge/values-preview"

	CacheStoreParamName        = "/ocel/edge/cache-store"
	CacheStorePreviewParamName = "/ocel/edge/cache-store-preview"

	ISRWriterParamName        = "/ocel/edge/isr-writer"
	ISRWriterPreviewParamName = "/ocel/edge/isr-writer-preview"

	ISRWriterSeedParamName        = "/ocel/edge/isr-writer-seed"
	ISRWriterSeedPreviewParamName = "/ocel/edge/isr-writer-seed-preview"
)

type IAMAPI interface {
	ListAccessKeys(ctx context.Context, in *iam.ListAccessKeysInput, optFns ...func(*iam.Options)) (*iam.ListAccessKeysOutput, error)
	CreateAccessKey(ctx context.Context, in *iam.CreateAccessKeyInput, optFns ...func(*iam.Options)) (*iam.CreateAccessKeyOutput, error)
}

type EdgeCredentials struct {
	AccessKeyID     string `json:"accessKeyId"`
	SecretAccessKey string `json:"secretAccessKey"`
}

type edgeNames struct {
	user                  string
	credentialsParam      string
	valuesParam           string
	cacheStoreParam       string
	deploymentsStoreParam string
	isrWriterParam        string
	isrWriterSeedParam    string
}

var edgeNamesByClass = map[string]edgeNames{
	ClassProduction: {EdgeUserName, EdgeCredentialsParamName, EdgeValuesParamName, CacheStoreParamName, DeploymentsStoreParamName, ISRWriterParamName, ISRWriterSeedParamName},
	ClassPreview:    {EdgePreviewUserName, EdgeCredentialsPreviewParamName, EdgeValuesPreviewParamName, CacheStorePreviewParamName, DeploymentsStorePreviewParamName, ISRWriterPreviewParamName, ISRWriterSeedPreviewParamName},
}

func edgeNamesFor(class string) (edgeNames, error) {
	names, ok := edgeNamesByClass[class]
	if !ok {
		return edgeNames{}, fmt.Errorf("edge: unknown substrate class %q", class)
	}
	return names, nil
}

func DeploymentsStoreParamFor(class string) (string, error) {
	names, err := edgeNamesFor(class)
	if err != nil {
		return "", err
	}
	return names.deploymentsStoreParam, nil
}

func ISRWriterParamFor(class string) (string, error) {
	names, err := edgeNamesFor(class)
	if err != nil {
		return "", err
	}
	return names.isrWriterParam, nil
}

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

type CacheStore struct {
	Bucket          string `json:"bucket"`
	Endpoint        string `json:"endpoint"`
	Region          string `json:"region"`
	AccessKeyID     string `json:"accessKeyId"`
	SecretAccessKey string `json:"secretAccessKey"`
}

const (
	DeploymentsStoreParamName        = "/ocel/edge/deployments-store"
	DeploymentsStorePreviewParamName = "/ocel/edge/deployments-store-preview"
)

type DeploymentsStore struct {
	Endpoint      string `json:"endpoint"`
	ScriptName    string `json:"scriptName"`
	BootstrapCred string `json:"bootstrapCred"`
}

func adoptDeploymentsStore(ctx context.Context, ssmClient SSMAPI, class string, values map[string]string) error {
	paramName, err := DeploymentsStoreParamFor(class)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(DeploymentsStore{
		Endpoint:      values[edge.OfferKeyStoreEndpoint],
		ScriptName:    values[edge.OfferKeyStoreScriptName],
		BootstrapCred: values[edge.OfferKeyStoreBootstrapCred],
	})
	if err != nil {
		return fmt.Errorf("marshal deployments store: %w", err)
	}
	if _, err := ssmClient.PutParameter(ctx, &ssm.PutParameterInput{
		Name:      aws.String(paramName),
		Value:     aws.String(string(payload)),
		Type:      ssmtypes.ParameterTypeSecureString,
		Overwrite: aws.Bool(true),
	}); err != nil {
		return fmt.Errorf("write deployments store parameter: %w", err)
	}
	return nil
}

func ReadDeploymentsStoreFor(ctx context.Context, ssmClient SSMAPI, class string) (DeploymentsStore, error) {
	paramName, err := DeploymentsStoreParamFor(class)
	if err != nil {
		return DeploymentsStore{}, err
	}
	out, err := ssmClient.GetParameter(ctx, &ssm.GetParameterInput{
		Name:           aws.String(paramName),
		WithDecryption: aws.Bool(true),
	})
	if err != nil {
		var notFound *ssmtypes.ParameterNotFound
		if errors.As(err, &notFound) {
			return DeploymentsStore{}, nil
		}
		return DeploymentsStore{}, fmt.Errorf("read deployments store parameter: %w", err)
	}
	var store DeploymentsStore
	if err := json.Unmarshal([]byte(aws.ToString(out.Parameter.Value)), &store); err != nil {
		return DeploymentsStore{}, fmt.Errorf("parse deployments store: %w", err)
	}
	return store, nil
}

type ISRWriter struct {
	Endpoint      string `json:"endpoint"`
	ScriptName    string `json:"scriptName"`
	BootstrapCred string `json:"bootstrapCred"`
}

func adoptISRWriter(ctx context.Context, ssmClient SSMAPI, class string, values map[string]string) error {
	paramName, err := ISRWriterParamFor(class)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(ISRWriter{
		Endpoint:      values[edge.OfferKeyISRWriterEndpoint],
		ScriptName:    values[edge.OfferKeyISRWriterScriptName],
		BootstrapCred: values[edge.OfferKeyISRWriterBootstrapCred],
	})
	if err != nil {
		return fmt.Errorf("marshal isr writer: %w", err)
	}
	if _, err := ssmClient.PutParameter(ctx, &ssm.PutParameterInput{
		Name:      aws.String(paramName),
		Value:     aws.String(string(payload)),
		Type:      ssmtypes.ParameterTypeSecureString,
		Overwrite: aws.Bool(true),
	}); err != nil {
		return fmt.Errorf("write isr writer parameter: %w", err)
	}
	return nil
}

func ReadISRWriterFor(ctx context.Context, ssmClient SSMAPI, class string) (ISRWriter, error) {
	paramName, err := ISRWriterParamFor(class)
	if err != nil {
		return ISRWriter{}, err
	}
	out, err := ssmClient.GetParameter(ctx, &ssm.GetParameterInput{
		Name:           aws.String(paramName),
		WithDecryption: aws.Bool(true),
	})
	if err != nil {
		var notFound *ssmtypes.ParameterNotFound
		if errors.As(err, &notFound) {
			return ISRWriter{}, nil
		}
		return ISRWriter{}, fmt.Errorf("read isr writer parameter: %w", err)
	}
	var writer ISRWriter
	if err := json.Unmarshal([]byte(aws.ToString(out.Parameter.Value)), &writer); err != nil {
		return ISRWriter{}, fmt.Errorf("parse isr writer: %w", err)
	}
	return writer, nil
}

func ISRWriterSeedParamFor(class string) (string, error) {
	names, err := edgeNamesFor(class)
	if err != nil {
		return "", err
	}
	return names.isrWriterSeedParam, nil
}

func ensureISRWriterSeed(ctx context.Context, ssmClient SSMAPI, class string) (string, error) {
	paramName, err := ISRWriterSeedParamFor(class)
	if err != nil {
		return "", err
	}
	out, err := ssmClient.GetParameter(ctx, &ssm.GetParameterInput{
		Name:           aws.String(paramName),
		WithDecryption: aws.Bool(true),
	})
	if err == nil {
		return aws.ToString(out.Parameter.Value), nil
	}
	var notFound *ssmtypes.ParameterNotFound
	if !errors.As(err, &notFound) {
		return "", fmt.Errorf("read isr writer seed parameter: %w", err)
	}

	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate isr writer seed: %w", err)
	}
	seed := hex.EncodeToString(buf)
	if _, err := ssmClient.PutParameter(ctx, &ssm.PutParameterInput{
		Name:      aws.String(paramName),
		Value:     aws.String(seed),
		Type:      ssmtypes.ParameterTypeSecureString,
		Overwrite: aws.Bool(false),
	}); err != nil {
		var exists *ssmtypes.ParameterAlreadyExists
		if !errors.As(err, &exists) {
			return "", fmt.Errorf("write isr writer seed parameter: %w", err)
		}
		out, err := ssmClient.GetParameter(ctx, &ssm.GetParameterInput{
			Name:           aws.String(paramName),
			WithDecryption: aws.Bool(true),
		})
		if err != nil {
			return "", fmt.Errorf("read isr writer seed parameter a concurrent bootstrap created: %w", err)
		}
		return aws.ToString(out.Parameter.Value), nil
	}
	return seed, nil
}

func ReadISRWriterSeedFor(ctx context.Context, ssmClient SSMAPI, class string) (string, error) {
	paramName, err := ISRWriterSeedParamFor(class)
	if err != nil {
		return "", err
	}
	out, err := ssmClient.GetParameter(ctx, &ssm.GetParameterInput{
		Name:           aws.String(paramName),
		WithDecryption: aws.Bool(true),
	})
	if err != nil {
		var notFound *ssmtypes.ParameterNotFound
		if errors.As(err, &notFound) {
			return "", nil
		}
		return "", fmt.Errorf("read isr writer seed parameter: %w", err)
	}
	return aws.ToString(out.Parameter.Value), nil
}

func adoptCacheStore(ctx context.Context, ssmClient SSMAPI, class string, kind edge.Kind, values map[string]string) error {
	names, err := edgeNamesFor(class)
	if err != nil {
		return err
	}
	store := CacheStore{
		Bucket:          values[edge.OfferKeyBucket],
		Endpoint:        values[edge.OfferKeyEndpoint],
		Region:          values[edge.OfferKeyRegion],
		AccessKeyID:     values[edge.OfferKeyAccessKeyID],
		SecretAccessKey: values[edge.OfferKeySecretAccessKey],
	}

	if store.SecretAccessKey == "" {
		stored, err := ReadCacheStore(ctx, ssmClient, class)
		if err != nil {
			return err
		}
		if stored.AccessKeyID != store.AccessKeyID || stored.SecretAccessKey == "" {
			return fmt.Errorf(
				"the %s edge reoffered cache-store credential %q without a secret, but %s holds no secret for it: "+
					"a prior bootstrap minted that credential and failed before storing it. Its secret cannot be read "+
					"back, so delete credential %q for bucket %q at the %s edge and re-run bootstrap to mint a fresh one",
				kind, store.AccessKeyID, names.cacheStoreParam, store.AccessKeyID, store.Bucket, kind,
			)
		}
		store.SecretAccessKey = stored.SecretAccessKey
	}

	payload, err := json.Marshal(store)
	if err != nil {
		return fmt.Errorf("marshal cache store: %w", err)
	}
	if _, err := ssmClient.PutParameter(ctx, &ssm.PutParameterInput{
		Name:      aws.String(names.cacheStoreParam),
		Value:     aws.String(string(payload)),
		Type:      ssmtypes.ParameterTypeSecureString,
		Overwrite: aws.Bool(true),
	}); err != nil {
		return fmt.Errorf("write cache store parameter: %w", err)
	}
	return nil
}

func ReadCacheStore(ctx context.Context, ssmClient SSMAPI, class string) (CacheStore, error) {
	names, err := edgeNamesFor(class)
	if err != nil {
		return CacheStore{}, err
	}
	out, err := ssmClient.GetParameter(ctx, &ssm.GetParameterInput{
		Name:           aws.String(names.cacheStoreParam),
		WithDecryption: aws.Bool(true),
	})
	if err != nil {
		var notFound *ssmtypes.ParameterNotFound
		if errors.As(err, &notFound) {
			return CacheStore{}, nil
		}
		return CacheStore{}, fmt.Errorf("read cache store parameter: %w", err)
	}
	var store CacheStore
	if err := json.Unmarshal([]byte(aws.ToString(out.Parameter.Value)), &store); err != nil {
		return CacheStore{}, fmt.Errorf("parse cache store: %w", err)
	}
	return store, nil
}
