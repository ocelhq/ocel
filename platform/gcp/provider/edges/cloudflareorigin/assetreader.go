package cloudflareorigin

import (
	"context"
	"errors"
	"fmt"
	"time"

	"cloud.google.com/go/storage"
	"google.golang.org/api/iterator"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/originsecret"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/bootstrap"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/grants"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/policy"
)

const (
	assetStoreKeyKind = "storage:hmackey"

	assetReaderName        = "asset reader"
	assetReaderDescription = "the identity an edge that runs code reads a tier's static files as, through an HMAC key, and nothing else"
)

func assetReaderCondition(ctx context.Context, c *gcloud.Clients, tier environment.Tier) (*policy.Condition, error) {
	buckets, err := c.TierBuckets(ctx, tier, naming.StorageEdge)
	if err != nil {
		return nil, err
	}
	return gcloud.BucketCondition("ocel "+string(c.Namespace())+" "+string(tier)+" assets", buckets[naming.StorageEdge]), nil
}

func RaiseAssetStore(ctx context.Context, c *gcloud.Clients, tier environment.Tier, kind edge.Kind) error {
	account := c.AssetReaderAccount(tier)
	if err := c.CreateAccount(ctx, tier, account, "ocel "+string(tier)+" "+assetReaderName, assetReaderDescription); err != nil {
		return err
	}
	member := "serviceAccount:" + c.AssetReaderAccountEmail(tier)
	condition, err := assetReaderCondition(ctx, c, tier)
	if err != nil {
		return err
	}
	if err := gcloud.UntilVisible(ctx, func() error {
		return gcloud.BindProjectRole(ctx, c, member, grants.AssetsRole, condition, true)
	}); err != nil {
		return err
	}
	return ensureAssetReaderKey(ctx, c, tier, kind)
}

func ensureAssetReaderKey(ctx context.Context, c *gcloud.Clients, tier environment.Tier, kind edge.Kind) error {
	credentials, err := ReadEdgeCredentials(ctx, c, tier, kind)
	if err != nil {
		return err
	}
	client, err := c.Storage()
	if err != nil {
		return err
	}
	email := c.AssetReaderAccountEmail(tier)
	if credentials.hasAssetStoreKey() {
		key, err := client.HMACKeyHandle(c.Project(), credentials.AssetStoreAccessKeyID).Get(ctx)
		switch {
		case err == nil && key.State == storage.Active && key.ServiceAccountEmail == email:
			return nil
		case err != nil && !gcloud.Absent(err):
			return fmt.Errorf("read the HMAC key %s of the %s service account: %w", credentials.AssetStoreAccessKeyID, email, err)
		}
	}
	key, err := client.CreateHMACKey(ctx, c.Project(), email)
	if err != nil {
		return fmt.Errorf("create an HMAC key for the %s service account: %w", email, err)
	}
	credentials.AssetStoreAccessKeyID, credentials.AssetStoreSecretAccessKey = key.AccessID, key.Secret
	if err := WriteEdgeCredentials(ctx, c, tier, kind, credentials); err != nil {
		return err
	}
	return deleteAssetReaderKeys(ctx, c, client, email, key.AccessID)
}

func deleteAssetReaderKeys(ctx context.Context, c *gcloud.Clients, client *storage.Client, email, keep string) error {
	keys := client.ListHMACKeys(ctx, c.Project(), storage.ForHMACKeyServiceAccountEmail(email))
	for {
		key, err := keys.Next()
		if errors.Is(err, iterator.Done) {
			return nil
		}
		if err != nil {
			if gcloud.Absent(err) {
				return nil
			}
			return fmt.Errorf("list the HMAC keys of the %s service account: %w", email, err)
		}
		if key.State == storage.Deleted || key.AccessID == keep {
			continue
		}
		handle := client.HMACKeyHandle(c.Project(), key.AccessID)
		if key.State == storage.Active {
			if _, err := handle.Update(ctx, storage.HMACKeyAttrsToUpdate{State: storage.Inactive}); err != nil && !gcloud.Absent(err) {
				return fmt.Errorf("deactivate the HMAC key %s: %w", key.AccessID, err)
			}
		}
		if err := handle.Delete(ctx); err != nil && !gcloud.Absent(err) {
			return fmt.Errorf("delete the HMAC key %s: %w", key.AccessID, err)
		}
	}
}

func TakeAssetStore(ctx context.Context, c *gcloud.Clients, tier environment.Tier) error {
	client, err := c.Storage()
	if err != nil {
		return err
	}
	if err := deleteAssetReaderKeys(ctx, c, client, c.AssetReaderAccountEmail(tier), ""); err != nil {
		return err
	}
	member := "serviceAccount:" + c.AssetReaderAccountEmail(tier)
	if _, err := policy.Edit(ctx, gcloud.ProjectPolicy(c), policy.RevokeRoles(member, []string{grants.AssetsRole})); err != nil {
		return err
	}
	_, err = c.DeleteAccount(ctx, tier, c.AssetReaderAccount(tier))
	return err
}

type Assets struct {
	Account    bool
	Key        bool
	Secret     originsecret.Secret
	SecretHeld bool
}

func ReadAssets(ctx context.Context, b bootstrap.Engine, tier environment.Tier, kind edge.Kind) (Assets, error) {
	c := b.Clients
	var held Assets
	var err error
	if !b.IsOff(bootstrap.IAMAPI) {
		if held.Account, err = c.AccountExists(ctx, c.AssetReaderAccount(tier)); err != nil {
			return Assets{}, err
		}
	}
	if b.IsOff(bootstrap.SecretManagerAPI) {
		return held, nil
	}
	if !b.IsOff(bootstrap.StorageAPI) {
		if held.Key, err = hasActiveAssetReaderKey(ctx, c, tier, kind); err != nil {
			return Assets{}, err
		}
	}
	held.Secret, held.SecretHeld, err = ReadOriginSecret(ctx, c, tier, kind)
	return held, err
}

func (a Assets) Planned(c *gcloud.Clients, tier environment.Tier, kind edge.Kind, now time.Time) []provider.Change {
	secret := provider.Change{Kind: string(bootstrap.KindSecret), Name: c.OriginSecret(tier, kind)}
	secret.Action, secret.Reason = a.Secret.PlanChange(now)
	return []provider.Change{
		{Kind: string(bootstrap.KindServiceAccount), Name: c.AssetReaderAccount(tier), Action: provider.KeepOrCreate(a.Account)},
		{Kind: assetStoreKeyKind, Name: c.AssetReaderAccountEmail(tier), Action: provider.KeepOrCreate(a.Key)},
		secret,
	}
}

func (a Assets) PlannedRemoval(c *gcloud.Clients, tier environment.Tier, kind edge.Kind) []provider.Change {
	return []provider.Change{
		plannedRemoval(string(bootstrap.KindServiceAccount), c.AssetReaderAccount(tier), a.Account),
		plannedRemoval(assetStoreKeyKind, c.AssetReaderAccountEmail(tier), a.Key),
		plannedRemoval(string(bootstrap.KindSecret), c.OriginSecret(tier, kind), a.SecretHeld),
	}
}

func plannedRemoval(kind, name string, held bool) provider.Change {
	if held {
		return provider.Change{Kind: kind, Name: name, Action: provider.ActionDelete}
	}
	return provider.Change{Kind: kind, Name: name, Action: provider.ActionKeep, Reason: bootstrap.ReasonAbsent}
}

func hasActiveAssetReaderKey(ctx context.Context, c *gcloud.Clients, tier environment.Tier, kind edge.Kind) (bool, error) {
	credentials, err := ReadEdgeCredentials(ctx, c, tier, kind)
	if err != nil || !credentials.hasAssetStoreKey() {
		return false, err
	}
	client, err := c.Storage()
	if err != nil {
		return false, err
	}
	key, err := client.HMACKeyHandle(c.Project(), credentials.AssetStoreAccessKeyID).Get(ctx)
	if gcloud.Absent(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read the HMAC key %s: %w", credentials.AssetStoreAccessKeyID, err)
	}
	return key.State == storage.Active, nil
}
