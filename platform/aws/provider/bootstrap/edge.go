package bootstrap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	iamtypes "github.com/aws/aws-sdk-go-v2/service/iam/types"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	ssmtypes "github.com/aws/aws-sdk-go-v2/service/ssm/types"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type IAMAPI interface {
	IAMKeyAPI
	CreateAccessKey(ctx context.Context, in *iam.CreateAccessKeyInput, optFns ...func(*iam.Options)) (*iam.CreateAccessKeyOutput, error)
	GetAccessKeyLastUsed(ctx context.Context, in *iam.GetAccessKeyLastUsedInput, optFns ...func(*iam.Options)) (*iam.GetAccessKeyLastUsedOutput, error)
}

type EdgeCredentials struct {
	AccessKeyID     string    `json:"accessKeyId"`
	SecretAccessKey string    `json:"secretAccessKey"`
	CreatedAt       time.Time `json:"createdAt,omitzero"`
}

const (
	EdgeKeyMaxAge      = 90 * 24 * time.Hour
	edgeKeyRetireGrace = 24 * time.Hour
)

func (c EdgeCredentials) Stale(now time.Time) bool {
	return !c.CreatedAt.IsZero() && now.Sub(c.CreatedAt) >= EdgeKeyMaxAge
}

func StaleEdgeKeyNotice(c EdgeCredentials, now time.Time, class string) string {
	if !c.Stale(now) {
		return ""
	}
	return fmt.Sprintf("the %s edge signs into this account with access key %s, minted %d days ago; run `ocel bootstrap` to rotate it (keys older than %d days are rotated there), then re-deploy each project so its worker picks the new key up",
		class, c.AccessKeyID, int(now.Sub(c.CreatedAt).Hours()/24), int(EdgeKeyMaxAge.Hours()/24))
}

type edgeKeyOutcome struct {
	minted  bool
	rotated bool
	retired string
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

func edgeNamesFor(ns Namespace, class string, kind edge.Kind) (edgeNames, error) {
	prefix, err := ns.EdgeParamPrefix(class, kind)
	if err != nil {
		return edgeNames{}, err
	}
	user, err := ns.EdgeUserNameFor(class)
	if err != nil {
		return edgeNames{}, err
	}
	secret, err := ns.OriginSecretParamFor(class)
	if err != nil {
		return edgeNames{}, err
	}
	return edgeNames{
		user:                  user,
		credentialsParam:      prefix + "/credentials",
		valuesParam:           prefix + "/values",
		cacheStoreParam:       prefix + "/cache-store",
		deploymentsStoreParam: prefix + "/deployments-store",
		isrWriterParam:        prefix + "/isr-writer",
		isrWriterSeedParam:    prefix + "/isr-writer-seed",
		originSecretParam:     secret,
	}, nil
}

func EdgeInstalled(ctx context.Context, ssmClient SSMAPI, ns Namespace, class string, kind edge.Kind) (bool, error) {
	names, err := edgeNamesFor(ns, class, kind)
	if err != nil {
		return false, err
	}
	for _, name := range names.edgeParams() {
		present, err := paramPresent(ctx, ssmClient, name)
		if err != nil || present {
			return present, err
		}
	}
	return false, nil
}

func DeploymentsStoreParamFor(ns Namespace, class string, kind edge.Kind) (string, error) {
	names, err := edgeNamesFor(ns, class, kind)
	if err != nil {
		return "", err
	}
	return names.deploymentsStoreParam, nil
}

func ISRWriterParamFor(ns Namespace, class string, kind edge.Kind) (string, error) {
	names, err := edgeNamesFor(ns, class, kind)
	if err != nil {
		return "", err
	}
	return names.isrWriterParam, nil
}

func writeEdgeValues(ctx context.Context, ssmClient SSMAPI, ns Namespace, class string, kind edge.Kind, values map[string]string) error {
	names, err := edgeNamesFor(ns, class, kind)
	if err != nil {
		return err
	}
	stored, err := ReadEdgeValues(ctx, ssmClient, ns, class, kind)
	if err != nil {
		return err
	}
	if maps.Equal(stored, values) {
		return nil
	}
	payload, err := json.Marshal(values)
	if err != nil {
		return fmt.Errorf("marshal edge values: %w", err)
	}
	if _, err := ssmClient.PutParameter(ctx, &ssm.PutParameterInput{
		Name:        aws.String(names.valuesParam),
		Description: aws.String(fmt.Sprintf("Ocel: everything the %s edge handed back when it was bootstrapped, read on every deploy into this bootstrap to reach it. Rewritten in full by each bootstrap of this class.", class)),
		Value:       aws.String(string(payload)),
		Type:        ssmtypes.ParameterTypeSecureString,
		Overwrite:   aws.Bool(true),
	}); err != nil {
		return fmt.Errorf("write edge values parameter: %w", err)
	}
	return nil
}

func ReadEdgeValues(ctx context.Context, ssmClient SSMAPI, ns Namespace, class string, kind edge.Kind) (map[string]string, error) {
	names, err := edgeNamesFor(ns, class, kind)
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

func ensureEdgeCredentials(ctx context.Context, iamClient IAMAPI, ssmClient SSMAPI, ns Namespace, class string, kind edge.Kind, now time.Time) (edgeKeyOutcome, error) {
	names, err := edgeNamesFor(ns, class, kind)
	if err != nil {
		return edgeKeyOutcome{}, err
	}
	paramName, userName := names.credentialsParam, names.user

	recorded, _, err := recordedEdgeKey(ctx, ssmClient, paramName)
	if err != nil {
		return edgeKeyOutcome{}, err
	}

	keys, err := iamClient.ListAccessKeys(ctx, &iam.ListAccessKeysInput{
		UserName: aws.String(userName),
	})
	if err != nil {
		return edgeKeyOutcome{}, fmt.Errorf("list edge access keys for %s: %w", userName, err)
	}
	live := slices.IndexFunc(keys.AccessKeyMetadata, func(key iamtypes.AccessKeyMetadata) bool {
		return aws.ToString(key.AccessKeyId) == recorded.AccessKeyID
	})
	if live < 0 {
		if len(keys.AccessKeyMetadata) >= 2 {
			return edgeKeyOutcome{}, fmt.Errorf(
				"edge reader %s already has %d access keys but %s: delete a stale key with "+
					"iam.DeleteAccessKey, then re-run bootstrap",
				userName, len(keys.AccessKeyMetadata), strandedKeys(recorded.AccessKeyID, paramName),
			)
		}
		if err := mintEdgeKey(ctx, iamClient, ssmClient, paramName, userName, class, now); err != nil {
			return edgeKeyOutcome{}, err
		}
		return edgeKeyOutcome{minted: true}, nil
	}

	var outcome edgeKeyOutcome
	for _, key := range keys.AccessKeyMetadata {
		if id := aws.ToString(key.AccessKeyId); id != recorded.AccessKeyID {
			retired, err := retireIdleEdgeKey(ctx, iamClient, userName, id, now)
			if err != nil {
				return edgeKeyOutcome{}, err
			}
			if retired {
				outcome.retired = id
			}
		}
	}

	minted := recorded.CreatedAt
	if minted.IsZero() {
		minted = aws.ToTime(keys.AccessKeyMetadata[live].CreateDate)
	}
	if minted.IsZero() || now.Sub(minted) < EdgeKeyMaxAge {
		return outcome, nil
	}
	if len(keys.AccessKeyMetadata) >= 2 && outcome.retired == "" {
		return edgeKeyOutcome{}, fmt.Errorf(
			"edge access key %s of %s is %d days old and due for rotation, but the user's other key is still signing calls, so there is no room for a fresh one: "+
				"re-deploy every project onto %s, wait %s, and re-run bootstrap",
			recorded.AccessKeyID, userName, int(now.Sub(minted).Hours()/24), recorded.AccessKeyID, edgeKeyRetireGrace,
		)
	}
	if err := mintEdgeKey(ctx, iamClient, ssmClient, paramName, userName, class, now); err != nil {
		return edgeKeyOutcome{}, err
	}
	outcome.minted, outcome.rotated = true, true
	return outcome, nil
}

func retireIdleEdgeKey(ctx context.Context, iamClient IAMAPI, userName, keyID string, now time.Time) (bool, error) {
	used, err := iamClient.GetAccessKeyLastUsed(ctx, &iam.GetAccessKeyLastUsedInput{AccessKeyId: aws.String(keyID)})
	if err != nil {
		return false, fmt.Errorf("read when edge access key %s was last used: %w", keyID, err)
	}
	if used.AccessKeyLastUsed != nil && used.AccessKeyLastUsed.LastUsedDate != nil &&
		now.Sub(aws.ToTime(used.AccessKeyLastUsed.LastUsedDate)) < edgeKeyRetireGrace {
		return false, nil
	}
	if _, err := iamClient.DeleteAccessKey(ctx, &iam.DeleteAccessKeyInput{
		UserName:    aws.String(userName),
		AccessKeyId: aws.String(keyID),
	}); err != nil {
		var gone *iamtypes.NoSuchEntityException
		if errors.As(err, &gone) {
			return false, nil
		}
		return false, fmt.Errorf("retire the superseded edge access key %s: %w", keyID, err)
	}
	return true, nil
}

func mintEdgeKey(ctx context.Context, iamClient IAMAPI, ssmClient SSMAPI, paramName, userName, class string, now time.Time) error {
	out, err := iamClient.CreateAccessKey(ctx, &iam.CreateAccessKeyInput{
		UserName: aws.String(userName),
	})
	if err != nil {
		return fmt.Errorf("mint edge access key for %s: %w", userName, err)
	}
	payload, err := json.Marshal(EdgeCredentials{
		AccessKeyID:     aws.ToString(out.AccessKey.AccessKeyId),
		SecretAccessKey: aws.ToString(out.AccessKey.SecretAccessKey),
		CreatedAt:       now.UTC().Truncate(time.Second),
	})
	if err != nil {
		return fmt.Errorf("marshal edge credentials: %w", err)
	}
	if _, err := ssmClient.PutParameter(ctx, &ssm.PutParameterInput{
		Name:        aws.String(paramName),
		Description: aws.String(fmt.Sprintf("Ocel: the access key for IAM user %s, the identity the %s edge signs its calls into this account with, and when it was minted. This is the only copy of the secret - deleting it orphans the key on the user.", userName, class)),
		Value:       aws.String(string(payload)),
		Type:        ssmtypes.ParameterTypeSecureString,
		Overwrite:   aws.Bool(true),
	}); err != nil {
		return fmt.Errorf("write edge credentials parameter: %w", err)
	}
	return nil
}

func recordedEdgeKey(ctx context.Context, ssmClient SSMAPI, paramName string) (creds EdgeCredentials, present bool, err error) {
	out, err := ssmClient.GetParameter(ctx, &ssm.GetParameterInput{
		Name:           aws.String(paramName),
		WithDecryption: aws.Bool(true),
	})
	if err != nil {
		var notFound *ssmtypes.ParameterNotFound
		if errors.As(err, &notFound) {
			return EdgeCredentials{}, false, nil
		}
		return EdgeCredentials{}, false, fmt.Errorf("read edge credentials parameter: %w", err)
	}
	if err := json.Unmarshal([]byte(aws.ToString(out.Parameter.Value)), &creds); err != nil {
		return EdgeCredentials{}, true, fmt.Errorf("parse edge credentials in %s: %w", paramName, err)
	}
	return creds, true, nil
}

func strandedKeys(recorded, paramName string) string {
	if recorded == "" {
		return fmt.Sprintf("none is recorded in %s, so a prior mint likely failed before its PutParameter", paramName)
	}
	return fmt.Sprintf("%s, the one %s records, is not among them", recorded, paramName)
}

func ReadEdgeCredentials(ctx context.Context, ssmClient SSMAPI, ns Namespace, class string, kind edge.Kind) (EdgeCredentials, error) {
	names, err := edgeNamesFor(ns, class, kind)
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

type DeploymentsStore struct {
	Endpoint      string `json:"endpoint"`
	ScriptName    string `json:"scriptName"`
	BootstrapCred string `json:"bootstrapCred"`
}

func adoptDeploymentsStore(ctx context.Context, ssmClient SSMAPI, ns Namespace, class string, kind edge.Kind, values map[string]string) error {
	paramName, err := DeploymentsStoreParamFor(ns, class, kind)
	if err != nil {
		return err
	}
	store := DeploymentsStore{
		Endpoint:      values[edge.OfferKeyStoreEndpoint],
		ScriptName:    values[edge.OfferKeyStoreScriptName],
		BootstrapCred: values[edge.OfferKeyStoreBootstrapCred],
	}
	stored, err := ReadDeploymentsStoreFor(ctx, ssmClient, ns, class, kind)
	if err != nil {
		return err
	}
	if store.BootstrapCred == "" {
		if store.BootstrapCred = stored.BootstrapCred; store.BootstrapCred == "" {
			return edgeCredUnrecorded(kind, "deployments store", store.ScriptName, paramName)
		}
	}
	if store == stored {
		return nil
	}
	payload, err := json.Marshal(store)
	if err != nil {
		return fmt.Errorf("marshal deployments store: %w", err)
	}
	if _, err := ssmClient.PutParameter(ctx, &ssm.PutParameterInput{
		Name:        aws.String(paramName),
		Description: aws.String(fmt.Sprintf("Ocel: endpoint and bootstrap credential for the %s edge's deployments store, the worker that tells the edge which build a request belongs to. Every deploy publishes its routing through it.", class)),
		Value:       aws.String(string(payload)),
		Type:        ssmtypes.ParameterTypeSecureString,
		Overwrite:   aws.Bool(true),
	}); err != nil {
		return fmt.Errorf("write deployments store parameter: %w", err)
	}
	return nil
}

func edgeCredUnrecorded(kind edge.Kind, surface, scriptName, paramName string) error {
	return fmt.Errorf(
		"the %s edge reoffered its %s %q without a bootstrap credential, meaning it still has the one it was given, "+
			"but %s stores none: a prior bootstrap set that credential and failed before storing it. It cannot be read "+
			"back, so delete the bootstrap credential set on %q at the %s edge and re-run bootstrap to mint a fresh one",
		kind, surface, scriptName, paramName, scriptName, kind)
}

func ReadDeploymentsStoreFor(ctx context.Context, ssmClient SSMAPI, ns Namespace, class string, kind edge.Kind) (DeploymentsStore, error) {
	paramName, err := DeploymentsStoreParamFor(ns, class, kind)
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

func adoptISRWriter(ctx context.Context, ssmClient SSMAPI, ns Namespace, class string, kind edge.Kind, values map[string]string) error {
	paramName, err := ISRWriterParamFor(ns, class, kind)
	if err != nil {
		return err
	}
	writer := ISRWriter{
		Endpoint:      values[edge.OfferKeyISRWriterEndpoint],
		ScriptName:    values[edge.OfferKeyISRWriterScriptName],
		BootstrapCred: values[edge.OfferKeyISRWriterBootstrapCred],
	}
	stored, err := ReadISRWriterFor(ctx, ssmClient, ns, class, kind)
	if err != nil {
		return err
	}
	if writer.BootstrapCred == "" {
		if writer.BootstrapCred = stored.BootstrapCred; writer.BootstrapCred == "" {
			return edgeCredUnrecorded(kind, "ISR writer", writer.ScriptName, paramName)
		}
	}
	if writer == stored {
		return nil
	}
	payload, err := json.Marshal(writer)
	if err != nil {
		return fmt.Errorf("marshal isr writer: %w", err)
	}
	if _, err := ssmClient.PutParameter(ctx, &ssm.PutParameterInput{
		Name:        aws.String(paramName),
		Description: aws.String(fmt.Sprintf("Ocel: endpoint and bootstrap credential for the %s edge's ISR writer, the worker the tag publisher pushes tag snapshots into. Read at runtime by the tag publisher.", class)),
		Value:       aws.String(string(payload)),
		Type:        ssmtypes.ParameterTypeSecureString,
		Overwrite:   aws.Bool(true),
	}); err != nil {
		return fmt.Errorf("write isr writer parameter: %w", err)
	}
	return nil
}

func ReadISRWriterFor(ctx context.Context, ssmClient SSMAPI, ns Namespace, class string, kind edge.Kind) (ISRWriter, error) {
	paramName, err := ISRWriterParamFor(ns, class, kind)
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

func ISRWriterSeedParamFor(ns Namespace, class string, kind edge.Kind) (string, error) {
	names, err := edgeNamesFor(ns, class, kind)
	if err != nil {
		return "", err
	}
	return names.isrWriterSeedParam, nil
}

func ensureISRWriterSeed(ctx context.Context, ssmClient SSMAPI, ns Namespace, class string, kind edge.Kind) (string, error) {
	paramName, err := ISRWriterSeedParamFor(ns, class, kind)
	if err != nil {
		return "", err
	}
	return ensureSecret(ctx, ssmClient, paramName, fmt.Sprintf("Ocel: the shared secret the tag publisher authenticates its writes to the %s edge's ISR writer with, read at runtime by name.", class))
}

func ReadISRWriterSeedFor(ctx context.Context, ssmClient SSMAPI, ns Namespace, class string, kind edge.Kind) (string, error) {
	paramName, err := ISRWriterSeedParamFor(ns, class, kind)
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

func adoptCacheStore(ctx context.Context, ssmClient SSMAPI, ns Namespace, class string, kind edge.Kind, values map[string]string) error {
	names, err := edgeNamesFor(ns, class, kind)
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

	stored, err := ReadCacheStore(ctx, ssmClient, ns, class, kind)
	if err != nil {
		return err
	}
	if store.SecretAccessKey == "" {
		if stored.AccessKeyID != store.AccessKeyID || stored.SecretAccessKey == "" {
			return fmt.Errorf(
				"the %s edge reoffered cache-store credential %q without a secret, but %s stores no secret for it: "+
					"a prior bootstrap minted that credential and failed before storing it. Its secret cannot be read "+
					"back, so delete credential %q for bucket %q at the %s edge and re-run bootstrap to mint a fresh one",
				kind, store.AccessKeyID, names.cacheStoreParam, store.AccessKeyID, store.Bucket, kind,
			)
		}
		store.SecretAccessKey = stored.SecretAccessKey
	}
	if store == stored {
		return nil
	}

	payload, err := json.Marshal(store)
	if err != nil {
		return fmt.Errorf("marshal cache store: %w", err)
	}
	if _, err := ssmClient.PutParameter(ctx, &ssm.PutParameterInput{
		Name:        aws.String(names.cacheStoreParam),
		Description: aws.String(fmt.Sprintf("Ocel: bucket, endpoint and credentials for the %s edge's cache store, where the edge keeps the fetch cache it serves from. This is the only copy of that credential's secret; the %s edge cannot show it again.", class, kind)),
		Value:       aws.String(string(payload)),
		Type:        ssmtypes.ParameterTypeSecureString,
		Overwrite:   aws.Bool(true),
	}); err != nil {
		return fmt.Errorf("write cache store parameter: %w", err)
	}
	return nil
}

func ReadCacheStore(ctx context.Context, ssmClient SSMAPI, ns Namespace, class string, kind edge.Kind) (CacheStore, error) {
	names, err := edgeNamesFor(ns, class, kind)
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
