package bootstrap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	iamtypes "github.com/aws/aws-sdk-go-v2/service/iam/types"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	ssmtypes "github.com/aws/aws-sdk-go-v2/service/ssm/types"

	"github.com/ocelhq/ocel/platform/aws/provider/domains"
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

	OriginSecretParamName        = "/ocel/origin/secret"
	OriginSecretPreviewParamName = "/ocel/origin/secret-preview"

	PreviewDomainParamName = "/ocel/edge/preview-domain"
)

type PreviewDomain struct {
	BaseDomain string             `json:"baseDomain"`
	Edge       edge.Kind          `json:"edge"`
	Scope      string             `json:"scope"`
	GrammarMin uint32             `json:"grammarMin"`
	GrammarMax uint32             `json:"grammarMax"`
	Settlement domains.Settlement `json:"settlement,omitzero"`
}

func (d PreviewDomain) Wildcard() domains.Host {
	return d.Settlement.Host(string(edge.PreviewWildcard(d.BaseDomain)))
}

func (d PreviewDomain) Holder() (edge.Kind, bool) {
	if d.Edge != "" {
		return d.Edge, true
	}
	return "", false
}

func previewDomainParamFor(class string) (string, error) {
	if class != ClassPreview {
		return "", fmt.Errorf("preview domain: only the %s substrate class has one, not %q", ClassPreview, class)
	}
	return PreviewDomainParamName, nil
}

func WritePreviewDomain(ctx context.Context, ssmClient SSMAPI, class string, domain PreviewDomain) error {
	paramName, err := previewDomainParamFor(class)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(domain)
	if err != nil {
		return fmt.Errorf("marshal preview domain: %w", err)
	}
	if _, err := ssmClient.PutParameter(ctx, &ssm.PutParameterInput{
		Name:        aws.String(paramName),
		Description: aws.String("Ocel: the domain every project without a preview domain of its own serves its previews on, and the edge account scope the shared entry holding its wildcard lives in. Written by `ocel domain use --preview` and read on every preview deploy. Delete it and those projects lose their preview hostnames until the domain is used again."),
		Value:       aws.String(string(payload)),
		Type:        ssmtypes.ParameterTypeSecureString,
		Overwrite:   aws.Bool(true),
	}); err != nil {
		return fmt.Errorf("write preview domain parameter: %w", err)
	}
	return nil
}

func ReadPreviewDomain(ctx context.Context, ssmClient SSMAPI, class string) (PreviewDomain, error) {
	paramName, err := previewDomainParamFor(class)
	if err != nil {
		return PreviewDomain{}, err
	}
	out, err := ssmClient.GetParameter(ctx, &ssm.GetParameterInput{
		Name:           aws.String(paramName),
		WithDecryption: aws.Bool(true),
	})
	if err != nil {
		var notFound *ssmtypes.ParameterNotFound
		if errors.As(err, &notFound) {
			return PreviewDomain{}, nil
		}
		return PreviewDomain{}, fmt.Errorf("read preview domain parameter: %w", err)
	}
	var domain PreviewDomain
	if err := json.Unmarshal([]byte(aws.ToString(out.Parameter.Value)), &domain); err != nil {
		return PreviewDomain{}, fmt.Errorf("parse preview domain: %w", err)
	}
	return domain, nil
}

func DeletePreviewDomain(ctx context.Context, ssmClient SSMAPI, class string) error {
	paramName, err := previewDomainParamFor(class)
	if err != nil {
		return err
	}
	if _, err := ssmClient.DeleteParameter(ctx, &ssm.DeleteParameterInput{Name: aws.String(paramName)}); err != nil {
		var notFound *ssmtypes.ParameterNotFound
		if errors.As(err, &notFound) {
			return nil
		}
		return fmt.Errorf("delete preview domain parameter: %w", err)
	}
	return nil
}

type IAMAPI interface {
	IAMKeyAPI
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
	originSecretParam     string
}

var edgeNamesByClass = map[string]edgeNames{
	ClassProduction: {EdgeUserName, EdgeCredentialsParamName, EdgeValuesParamName, CacheStoreParamName, DeploymentsStoreParamName, ISRWriterParamName, ISRWriterSeedParamName, OriginSecretParamName},
	ClassPreview:    {EdgePreviewUserName, EdgeCredentialsPreviewParamName, EdgeValuesPreviewParamName, CacheStorePreviewParamName, DeploymentsStorePreviewParamName, ISRWriterPreviewParamName, ISRWriterSeedPreviewParamName, OriginSecretPreviewParamName},
}

func (n edgeNames) edgeParams() []string {
	return []string{
		n.credentialsParam,
		n.valuesParam,
		n.cacheStoreParam,
		n.deploymentsStoreParam,
		n.isrWriterParam,
		n.isrWriterSeedParam,
	}
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
		Name:        aws.String(names.valuesParam),
		Description: aws.String(fmt.Sprintf("Ocel: everything the %s edge handed back when it was bootstrapped, read on every deploy into this substrate to reach it. Rewritten in full by each bootstrap of this class, so deleting it costs a re-run of ocel bootstrap and nothing more.", class)),
		Value:       aws.String(string(payload)),
		Type:        ssmtypes.ParameterTypeSecureString,
		Overwrite:   aws.Bool(true),
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

	recorded, err := recordedEdgeKeyID(ctx, ssmClient, paramName)
	if err != nil {
		return false, err
	}

	keys, err := iamClient.ListAccessKeys(ctx, &iam.ListAccessKeysInput{
		UserName: aws.String(userName),
	})
	if err != nil {
		return false, fmt.Errorf("list edge access keys for %s: %w", userName, err)
	}
	if recorded != "" && slices.ContainsFunc(keys.AccessKeyMetadata, func(key iamtypes.AccessKeyMetadata) bool {
		return aws.ToString(key.AccessKeyId) == recorded
	}) {
		return false, nil
	}
	if len(keys.AccessKeyMetadata) >= 2 {
		return false, fmt.Errorf(
			"edge reader %s already has %d access keys but %s: delete a stale key with "+
				"iam.DeleteAccessKey, then re-run bootstrap",
			userName, len(keys.AccessKeyMetadata), strandedKeys(recorded, paramName),
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
		Name:        aws.String(paramName),
		Description: aws.String(fmt.Sprintf("Ocel: the access key for IAM user %s, the identity the %s edge signs its calls into this account with. This is the only copy of the secret - AWS will not show it again - and bootstrap mints a fresh key whenever the one named here is no longer on the user, so deleting this parameter leaves an orphaned key that must be removed by hand.", userName, class)),
		Value:       aws.String(string(payload)),
		Type:        ssmtypes.ParameterTypeSecureString,
		Overwrite:   aws.Bool(true),
	}); err != nil {
		return false, fmt.Errorf("write edge credentials parameter: %w", err)
	}
	return true, nil
}

func recordedEdgeKeyID(ctx context.Context, ssmClient SSMAPI, paramName string) (string, error) {
	out, err := ssmClient.GetParameter(ctx, &ssm.GetParameterInput{
		Name:           aws.String(paramName),
		WithDecryption: aws.Bool(true),
	})
	if err != nil {
		var notFound *ssmtypes.ParameterNotFound
		if errors.As(err, &notFound) {
			return "", nil
		}
		return "", fmt.Errorf("read edge credentials parameter: %w", err)
	}
	var creds EdgeCredentials
	if err := json.Unmarshal([]byte(aws.ToString(out.Parameter.Value)), &creds); err != nil {
		return "", fmt.Errorf("parse edge credentials in %s: %w", paramName, err)
	}
	return creds.AccessKeyID, nil
}

func strandedKeys(recorded, paramName string) string {
	if recorded == "" {
		return fmt.Sprintf("none is recorded in %s, so a prior mint likely failed before its PutParameter", paramName)
	}
	return fmt.Sprintf("%s, the one %s records, is not among them", recorded, paramName)
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
		Name:        aws.String(paramName),
		Description: aws.String(fmt.Sprintf("Ocel: endpoint and bootstrap credential for the %s edge's deployments store, the worker that tells the edge which build a request belongs to. Every deploy into this substrate publishes its routing through it. Re-adopted whole by ocel bootstrap, so deleting it costs a re-run.", class)),
		Value:       aws.String(string(payload)),
		Type:        ssmtypes.ParameterTypeSecureString,
		Overwrite:   aws.Bool(true),
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
		Name:        aws.String(paramName),
		Description: aws.String(fmt.Sprintf("Ocel: endpoint and bootstrap credential for the %s edge's ISR writer, the worker the tag publisher pushes tag snapshots into so the edge learns a cached page has gone stale. Read at runtime by the tag publisher. Re-adopted whole by ocel bootstrap, so deleting it costs a re-run.", class)),
		Value:       aws.String(string(payload)),
		Type:        ssmtypes.ParameterTypeSecureString,
		Overwrite:   aws.Bool(true),
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
	return ensureSecret(ctx, ssmClient, paramName, fmt.Sprintf("Ocel: the shared secret the tag publisher authenticates its writes to the %s edge's ISR writer with. Generated once and never rotated; the publisher reads it at runtime by name. Delete it and the next bootstrap generates a different secret, which the edge side will not recognise until it is bootstrapped again.", class))
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
		Name:        aws.String(names.cacheStoreParam),
		Description: aws.String(fmt.Sprintf("Ocel: bucket, endpoint and credentials for the %s edge's cache store, where the edge keeps the fetch cache it serves from. This is the only copy of that credential's secret - the %s edge cannot show it again - so deleting this parameter forces the stale credential to be deleted at the edge and a fresh one minted.", class, kind)),
		Value:       aws.String(string(payload)),
		Type:        ssmtypes.ParameterTypeSecureString,
		Overwrite:   aws.Bool(true),
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
