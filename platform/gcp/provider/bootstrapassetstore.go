package gcp

import (
	"context"
	"errors"
	"fmt"

	"cloud.google.com/go/storage"
	"google.golang.org/api/cloudresourcemanager/v1"
	"google.golang.org/api/iterator"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
)

const (
	assetStoreKeyKind = "storage:hmackey"

	assetReaderName        = "asset reader"
	assetReaderDescription = "the identity an edge that runs code reads a tier's static files as, through an HMAC key, and nothing else"
)

func (c *clients) assetReaderCondition(tier environment.Tier) *cloudresourcemanager.Expr {
	return c.objectPrefixCondition("ocel "+string(c.Namespace())+" "+string(tier)+" assets", tier, provider.StoreAssets+"/")
}

func (c *clients) raiseAssetStore(ctx context.Context, tier environment.Tier, kind edge.Kind) error {
	account := c.AssetReaderAccount(tier)
	if err := c.createAccount(ctx, account, "ocel "+string(tier)+" "+assetReaderName, assetReaderDescription); err != nil {
		return err
	}
	member := "serviceAccount:" + c.AssetReaderAccountEmail(tier)
	if err := untilVisible(ctx, func() error {
		return c.bindProjectRole(ctx, member, appAssetsRole, c.assetReaderCondition(tier), true)
	}); err != nil {
		return err
	}
	return c.ensureAssetReaderKey(ctx, tier, kind)
}

func (c *clients) ensureAssetReaderKey(ctx context.Context, tier environment.Tier, kind edge.Kind) error {
	credentials, err := readEdgeCredentials(ctx, c, tier, kind)
	if err != nil {
		return err
	}
	client, err := c.Storage()
	if err != nil {
		return err
	}
	email := c.AssetReaderAccountEmail(tier)
	if credentials.AssetStoreAccessKeyID != "" && credentials.AssetStoreSecretAccessKey != "" {
		key, err := client.HMACKeyHandle(c.project, credentials.AssetStoreAccessKeyID).Get(ctx)
		switch {
		case err == nil && key.State == storage.Active && key.ServiceAccountEmail == email:
			return nil
		case err != nil && !absent(err):
			return fmt.Errorf("read the HMAC key %s of the %s service account: %w", credentials.AssetStoreAccessKeyID, email, err)
		}
	}
	key, err := client.CreateHMACKey(ctx, c.project, email)
	if err != nil {
		return fmt.Errorf("create an HMAC key for the %s service account: %w", email, err)
	}
	credentials.AssetStoreAccessKeyID, credentials.AssetStoreSecretAccessKey = key.AccessID, key.Secret
	if err := writeEdgeCredentials(ctx, c, tier, kind, credentials); err != nil {
		return err
	}
	return c.deleteAssetReaderKeys(ctx, client, email, key.AccessID)
}

func (c *clients) deleteAssetReaderKeys(ctx context.Context, client *storage.Client, email, keep string) error {
	keys := client.ListHMACKeys(ctx, c.project, storage.ForHMACKeyServiceAccountEmail(email))
	for {
		key, err := keys.Next()
		if errors.Is(err, iterator.Done) {
			return nil
		}
		if err != nil {
			if absent(err) {
				return nil
			}
			return fmt.Errorf("list the HMAC keys of the %s service account: %w", email, err)
		}
		if key.State == storage.Deleted || key.AccessID == keep {
			continue
		}
		handle := client.HMACKeyHandle(c.project, key.AccessID)
		if key.State == storage.Active {
			if _, err := handle.Update(ctx, storage.HMACKeyAttrsToUpdate{State: storage.Inactive}); err != nil && !absent(err) {
				return fmt.Errorf("deactivate the HMAC key %s: %w", key.AccessID, err)
			}
		}
		if err := handle.Delete(ctx); err != nil && !absent(err) {
			return fmt.Errorf("delete the HMAC key %s: %w", key.AccessID, err)
		}
	}
}

func (c *clients) takeAssetStore(ctx context.Context, tier environment.Tier) error {
	client, err := c.Storage()
	if err != nil {
		return err
	}
	if err := c.deleteAssetReaderKeys(ctx, client, c.AssetReaderAccountEmail(tier), ""); err != nil {
		return err
	}
	member := "serviceAccount:" + c.AssetReaderAccountEmail(tier)
	if err := c.bindProjectRole(ctx, member, appAssetsRole, c.assetReaderCondition(tier), false); err != nil {
		return err
	}
	_, err = c.deleteAccount(ctx, c.AssetReaderAccount(tier))
	return err
}

func (c *clients) plannedAssetStore(ctx context.Context, tier environment.Tier, kind edge.Kind) ([]provider.Change, error) {
	account := c.AssetReaderAccount(tier)
	accountAction, accountReason := provider.ActionCreate, assetReaderDescription
	present, err := c.accountExists(ctx, account)
	if err != nil {
		return nil, err
	}
	if present {
		accountAction, accountReason = provider.ActionKeep, provider.ReasonCurrent
	}
	keyAction, keyReason := provider.ActionCreate, "the key the edge's worker signs its reads of this tier's static files with"
	live, err := c.assetReaderKeyLive(ctx, tier, kind)
	if err != nil {
		return nil, err
	}
	if live {
		keyAction, keyReason = provider.ActionKeep, provider.ReasonCurrent
	}
	return []provider.Change{
		{Kind: string(KindServiceAccount), Name: account, Action: accountAction, Reason: accountReason},
		{Kind: assetStoreKeyKind, Name: c.AssetReaderAccountEmail(tier), Action: keyAction, Reason: keyReason},
	}, nil
}

func (c *clients) plannedAssetStoreRemoval(ctx context.Context, tier environment.Tier, kind edge.Kind) ([]provider.Change, error) {
	planned, err := c.plannedAssetStore(ctx, tier, kind)
	if err != nil {
		return nil, err
	}
	for i := range planned {
		if planned[i].Action == provider.ActionKeep {
			planned[i].Action, planned[i].Reason = provider.ActionDelete, assetReaderDescription
			continue
		}
		planned[i].Action, planned[i].Reason = provider.ActionKeep, reasonAbsent
	}
	return planned, nil
}

func (c *clients) assetReaderKeyLive(ctx context.Context, tier environment.Tier, kind edge.Kind) (bool, error) {
	credentials, err := readEdgeCredentials(ctx, c, tier, kind)
	if err != nil || credentials.AssetStoreAccessKeyID == "" || credentials.AssetStoreSecretAccessKey == "" {
		return false, err
	}
	client, err := c.Storage()
	if err != nil {
		return false, err
	}
	key, err := client.HMACKeyHandle(c.project, credentials.AssetStoreAccessKeyID).Get(ctx)
	if absent(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read the HMAC key %s: %w", credentials.AssetStoreAccessKeyID, err)
	}
	return key.State == storage.Active, nil
}
