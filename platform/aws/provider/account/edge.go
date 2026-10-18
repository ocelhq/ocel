package account

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	iamtypes "github.com/aws/aws-sdk-go-v2/service/iam/types"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	ssmtypes "github.com/aws/aws-sdk-go-v2/service/ssm/types"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
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

func StaleEdgeKeyNotice(c EdgeCredentials, now time.Time, tier environment.Tier) string {
	if !c.Stale(now) {
		return ""
	}
	return fmt.Sprintf("The %s edge signs into this account with access key %s, minted %d days ago; run `ocel bootstrap` to rotate it (keys older than %d days are rotated there), then re-deploy each project so its worker picks the new key up",
		tier, c.AccessKeyID, int(now.Sub(c.CreatedAt).Hours()/24), int(EdgeKeyMaxAge.Hours()/24))
}

type edgeKeyOutcome struct {
	minted  bool
	rotated bool
	retired string
}

type edgeNames struct {
	user               string
	credentialsParam   string
	releasesStoreParam string
	purgeSeedParam     string
	originSecretParam  string
}

func (n edgeNames) edgeParams() []string {
	return []string{
		n.credentialsParam,
		n.releasesStoreParam,
		n.purgeSeedParam,
	}
}

func edgeNamesFor(ns Namespace, tier environment.Tier, kind edge.Kind) (edgeNames, error) {
	prefix, err := ns.EdgeParamPrefix(tier, kind)
	if err != nil {
		return edgeNames{}, err
	}
	user, err := ns.EdgeUserNameFor(tier)
	if err != nil {
		return edgeNames{}, err
	}
	secret, err := ns.OriginSecretParamFor(tier)
	if err != nil {
		return edgeNames{}, err
	}
	return edgeNames{
		user:               user,
		credentialsParam:   prefix + "/credentials",
		releasesStoreParam: prefix + "/releases-store",
		purgeSeedParam:     prefix + "/purge-seed",
		originSecretParam:  secret,
	}, nil
}

func EdgeInstalled(ctx context.Context, ssmClient SSMAPI, ns Namespace, tier environment.Tier, kind edge.Kind) (bool, error) {
	names, err := edgeNamesFor(ns, tier, kind)
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

func ReleasesStoreParamFor(ns Namespace, tier environment.Tier, kind edge.Kind) (string, error) {
	names, err := edgeNamesFor(ns, tier, kind)
	if err != nil {
		return "", err
	}
	return names.releasesStoreParam, nil
}

func ensureEdgeCredentials(ctx context.Context, iamClient IAMAPI, ssmClient SSMAPI, ns Namespace, tier environment.Tier, kind edge.Kind, now time.Time) (edgeKeyOutcome, error) {
	names, err := edgeNamesFor(ns, tier, kind)
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
		if err := mintEdgeKey(ctx, iamClient, ssmClient, paramName, userName, tier, now); err != nil {
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
	if err := mintEdgeKey(ctx, iamClient, ssmClient, paramName, userName, tier, now); err != nil {
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

func mintEdgeKey(ctx context.Context, iamClient IAMAPI, ssmClient SSMAPI, paramName, userName string, tier environment.Tier, now time.Time) error {
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
		Description: aws.String(fmt.Sprintf("Ocel: the access key for IAM user %s, the identity the %s edge signs its calls into this account with, and when it was minted. This is the only copy of the secret - deleting it orphans the key on the user.", userName, tier)),
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

func ReadEdgeCredentials(ctx context.Context, ssmClient SSMAPI, ns Namespace, tier environment.Tier, kind edge.Kind) (EdgeCredentials, error) {
	names, err := edgeNamesFor(ns, tier, kind)
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

type ReleasesStore struct {
	Endpoint            string `json:"endpoint"`
	ScriptName          string `json:"scriptName"`
	BootstrapCredential string `json:"bootstrapCred"`
}

func adoptReleasesStore(ctx context.Context, ssmClient SSMAPI, ns Namespace, tier environment.Tier, kind edge.Kind, offered edge.ReleasesStore) error {
	paramName, err := ReleasesStoreParamFor(ns, tier, kind)
	if err != nil {
		return err
	}
	store := ReleasesStore{
		Endpoint:            offered.Endpoint,
		ScriptName:          offered.Name,
		BootstrapCredential: offered.BootstrapCredential,
	}
	stored, err := ReadReleasesStoreFor(ctx, ssmClient, ns, tier, kind)
	if err != nil {
		return err
	}
	if store.BootstrapCredential == "" {
		if store.BootstrapCredential = stored.BootstrapCredential; store.BootstrapCredential == "" {
			return edgeCredUnrecorded(kind, "releases store", store.ScriptName, paramName)
		}
	}
	if store == stored {
		return nil
	}
	payload, err := json.Marshal(store)
	if err != nil {
		return fmt.Errorf("marshal releases store: %w", err)
	}
	if _, err := ssmClient.PutParameter(ctx, &ssm.PutParameterInput{
		Name:        aws.String(paramName),
		Description: aws.String(fmt.Sprintf("Ocel: endpoint and bootstrap credential for the %s edge's releases store, the worker that tells the edge which build a request belongs to. Every deploy publishes its routing through it.", tier)),
		Value:       aws.String(string(payload)),
		Type:        ssmtypes.ParameterTypeSecureString,
		Overwrite:   aws.Bool(true),
	}); err != nil {
		return fmt.Errorf("write releases store parameter: %w", err)
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

func ReadReleasesStoreFor(ctx context.Context, ssmClient SSMAPI, ns Namespace, tier environment.Tier, kind edge.Kind) (ReleasesStore, error) {
	paramName, err := ReleasesStoreParamFor(ns, tier, kind)
	if err != nil {
		return ReleasesStore{}, err
	}
	out, err := ssmClient.GetParameter(ctx, &ssm.GetParameterInput{
		Name:           aws.String(paramName),
		WithDecryption: aws.Bool(true),
	})
	if err != nil {
		var notFound *ssmtypes.ParameterNotFound
		if errors.As(err, &notFound) {
			return ReleasesStore{}, nil
		}
		return ReleasesStore{}, fmt.Errorf("read releases store parameter: %w", err)
	}
	var store ReleasesStore
	if err := json.Unmarshal([]byte(aws.ToString(out.Parameter.Value)), &store); err != nil {
		return ReleasesStore{}, fmt.Errorf("parse releases store: %w", err)
	}
	return store, nil
}

func PurgeSeedParamFor(ns Namespace, tier environment.Tier, kind edge.Kind) (string, error) {
	names, err := edgeNamesFor(ns, tier, kind)
	if err != nil {
		return "", err
	}
	return names.purgeSeedParam, nil
}

func ensurePurgeSeed(ctx context.Context, ssmClient SSMAPI, ns Namespace, tier environment.Tier, kind edge.Kind) (string, error) {
	paramName, err := PurgeSeedParamFor(ns, tier, kind)
	if err != nil {
		return "", err
	}
	return ensureSecret(ctx, ssmClient, paramName, fmt.Sprintf("Ocel: the secret every release's purge secret on the %s edge is derived from. The tag invalidator signs its purges with it, read at runtime by name.", tier))
}
