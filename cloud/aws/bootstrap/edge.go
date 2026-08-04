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

	"github.com/ocelhq/ocel/cloud/edge"
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

	// CacheStoreParamName / CacheStorePreviewParamName are the SSM SecureString
	// parameters holding each substrate's adopted cache store.
	CacheStoreParamName        = "/ocel/edge/cache-store"
	CacheStorePreviewParamName = "/ocel/edge/cache-store-preview"

	// ISRWriterParamName / ISRWriterPreviewParamName are the SSM SecureString
	// parameters holding each substrate's adopted ISR writer worker coordinates.
	ISRWriterParamName        = "/ocel/edge/isr-writer"
	ISRWriterPreviewParamName = "/ocel/edge/isr-writer-preview"

	// ISRWriterSeedParamName / ISRWriterSeedPreviewParamName are the SSM
	// SecureString parameters holding each substrate's ISR write-secret seed.
	// Deliberately separate from the adopted-writer coordinates above, which
	// every bootstrap overwrites: see ensureISRWriterSeed.
	ISRWriterSeedParamName        = "/ocel/edge/isr-writer-seed"
	ISRWriterSeedPreviewParamName = "/ocel/edge/isr-writer-seed-preview"
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
// its IAM user and the SSM parameters holding its credentials, values, adopted
// cache store, adopted deployments store and adopted ISR writer.
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

// DeploymentsStoreParamFor returns the SSM parameter a substrate class's adopted
// deployments-store worker coordinates live in. Production and preview each
// bootstrap their own store worker, so the parameter is class-keyed.
func DeploymentsStoreParamFor(class string) (string, error) {
	names, err := edgeNamesFor(class)
	if err != nil {
		return "", err
	}
	return names.deploymentsStoreParam, nil
}

// ISRWriterParamFor returns the SSM parameter a substrate class's adopted ISR
// writer worker coordinates live in. Production and preview each bootstrap
// their own writer worker, so the parameter is class-keyed.
func ISRWriterParamFor(class string) (string, error) {
	names, err := edgeNamesFor(class)
	if err != nil {
		return "", err
	}
	return names.isrWriterParam, nil
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

// CacheStore is the JSON payload stored in SSM for an adopted OfferCacheStore:
// the object store a substrate's incremental cache is backed by, described in
// S3-compatible terms so any consumer addresses it with the client it already
// has.
type CacheStore struct {
	Bucket          string `json:"bucket"`
	Endpoint        string `json:"endpoint"`
	Region          string `json:"region"`
	AccessKeyID     string `json:"accessKeyId"`
	SecretAccessKey string `json:"secretAccessKey"`
}

// DeploymentsStoreParamName / DeploymentsStorePreviewParamName are the
// account-level SSM SecureString parameters holding each substrate's
// deployments-store worker coordinates. Production and preview bootstrap
// separate store workers so the two substrates never share promotion history or
// Durable Object state.
const (
	DeploymentsStoreParamName        = "/ocel/edge/deployments-store"
	DeploymentsStorePreviewParamName = "/ocel/edge/deployments-store-preview"
)

// DeploymentsStore is the JSON payload stored in SSM: the shared
// deployments-store worker's coordinates, read at deploy time so a project's
// root stack can service-bind, seed and reach its own instance.
type DeploymentsStore struct {
	Endpoint      string `json:"endpoint"`
	ScriptName    string `json:"scriptName"`
	BootstrapCred string `json:"bootstrapCred"`
}

// adoptDeploymentsStore persists the edge's offered deployments-store worker for
// one substrate class. Every coordinate — including the bootstrap credential —
// is the edge's current state and is overwritten on every run: the credential is
// re-minted each bootstrap and read fresh from here at deploy time, so there is
// no prior secret to preserve.
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

// ReadDeploymentsStoreFor returns a substrate class's adopted deployments-store
// worker coordinates, decrypted. An account whose edge offered none (a bootstrap
// predating the store) reads as the zero value rather than as a failure, so the
// deploy path can tell "no store, skip the root stack" from an error.
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

// ISRWriter is the JSON payload stored in SSM: the shared ISR writer worker's
// coordinates, read at deploy time so a deploy can seed its build's write
// secret and point that build's functions at the worker.
type ISRWriter struct {
	Endpoint      string `json:"endpoint"`
	ScriptName    string `json:"scriptName"`
	BootstrapCred string `json:"bootstrapCred"`
}

// adoptISRWriter persists the edge's offered ISR writer worker for one
// substrate class. Like the deployments store's coordinates, every field is the
// edge's current state and is overwritten on every run: the credential is
// re-minted each bootstrap and read fresh from here at deploy time, so there is
// no prior secret to preserve.
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

// ReadISRWriterFor returns a substrate class's adopted ISR writer worker
// coordinates, decrypted. An account whose edge offered none (a bootstrap
// predating the writer) reads as the zero value rather than as a failure, so
// the deploy path can tell "no writer" from an error.
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

// ISRWriterSeedParamFor returns the SSM parameter a substrate class's ISR
// write-secret seed lives in. Exported so the deploy path can name it in the
// tag publisher's environment and scope that function's read grant to it.
func ISRWriterSeedParamFor(class string) (string, error) {
	names, err := edgeNamesFor(class)
	if err != nil {
		return "", err
	}
	return names.isrWriterSeedParam, nil
}

// ensureISRWriterSeed creates the substrate's ISR write-secret seed if it does
// not exist, and never overwrites one. It returns the seed in force.
//
// Every build's write secret is HMAC(seed, isrPrefix) — see isrWriteSecret in
// the deploy package — and the writer worker stores only the hash, per build,
// from whichever deploy last initialized it. So the seed is account state, not
// run state: rotating it silently invalidates the secret every live build's
// functions hold, and each one 401s its cache writes until it is redeployed.
// Create-only is what makes the derivation stable, which is in turn what lets
// the stream consumer derive any build's secret rather than needing a second
// credential path of its own.
//
// It lives in a parameter of its own because adoptISRWriter overwrites its
// whole payload on every bootstrap.
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
		// A concurrent bootstrap created it between the read and this write. The
		// seed in force is whichever one landed, so this one converges on it
		// rather than failing a run over a race it already lost harmlessly.
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

// ReadISRWriterSeedFor returns a substrate class's ISR write-secret seed,
// decrypted. An account bootstrapped before the seed reads as empty rather than
// as a failure, which the deploy reports as a substrate with no usable writer.
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

// adoptCacheStore persists an edge's offered cache store for one substrate class.
// Coordinates are the edge's current state and are overwritten on every run; the
// secret is not, because an edge that cannot read a credential back reoffers the
// store without one and the stored secret is then the only copy in existence.
//
// A secretless offer whose access key id is not the one already stored is the
// cross-run counterpart of the dangling-key trap ensureEdgeCredentials guards
// against: a prior run minted that credential and failed before persisting it, so
// its secret is unrecoverable and no re-run can mint over it — the edge finds the
// credential present and reoffers it forever. Storing the coordinates anyway would
// leave a store nothing can authenticate against, so this fails with the one
// remedy that works: delete the credential at the edge and re-run.
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

// ReadCacheStore returns the substrate's adopted cache store, decrypted. A
// substrate whose edge offered none reads as the zero store rather than as a
// failure, which is how a consumer tells "stay on the provider's own store" from
// an error.
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
