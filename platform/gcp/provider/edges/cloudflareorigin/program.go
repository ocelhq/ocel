package cloudflareorigin

import (
	"context"
	"net/url"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/originsecret"
	"github.com/ocelhq/ocel/platform/gcp/provider/bucket"
)

const (
	storeSigningRegion = "auto"

	imageEndpointMaxRequestBytes = 32 << 20

	cloudRunThrottleMessage = "no available instance"
)

func (p Offers) Program(ctx context.Context, req provider.EdgeProgramRequest) (edge.Program, error) {
	c, err := p.OpenClients(ctx)
	if err != nil {
		return edge.Program{}, err
	}
	adopted, credentials, err := RequireAdoptedEdge(ctx, c, p.KeyValues, req.Tier, req.Kind)
	if err != nil {
		return edge.Program{}, err
	}
	if !credentials.hasAssetStoreKey() {
		return edge.Program{}, notBootstrapped(req.Tier, req.Kind, "no asset-store credential")
	}
	secret, err := RequireOriginSecret(ctx, c, req.Tier, req.Kind)
	if err != nil {
		return edge.Program{}, err
	}
	buckets, err := c.TierBuckets(ctx, req.Tier, naming.StorageEdge)
	if err != nil {
		return edge.Program{}, err
	}
	return edge.Program{
		Origin: edge.OriginReach{
			Reach: originSecretReach(secret),
			// TODO: the asset-store key is one account per tier that reads the whole edge
			// bucket, so every project's worker can read every other project's static files,
			// pages entries and edge bundles. Bind a reader per project to its own prefixes,
			// then set HoldsEntries so the edge serves pages entries from the store.
			Store: &edge.OriginStore{
				URL:             bucket.GoogleStorage + "/" + url.PathEscape(buckets[naming.StorageEdge]),
				Region:          storeSigningRegion,
				AccessKeyID:     credentials.AssetStoreAccessKeyID,
				SecretAccessKey: credentials.AssetStoreSecretAccessKey,
			},
			Throttle: edge.Throttle{Status: 429, BodyContains: cloudRunThrottleMessage},
			Images:   &edge.OriginImages{MaxRequestBytes: imageEndpointMaxRequestBytes},
		},
		ReleasesStore: edge.ReleasesStore{
			Name:                adopted.ReleasesStore.ScriptName,
			Endpoint:            adopted.ReleasesStore.Endpoint,
			BootstrapCredential: credentials.ReleasesStore,
		},
	}, nil
}

func originSecretReach(secret originsecret.Secret) edge.Reach {
	reach := edge.Reach{Kind: edge.ReachOriginSecret, Secret: secret.Current}
	if secret.Rotating() {
		reach.PreviousSecret = secret.Previous
		reach.PreviousFingerprint = edge.OriginSecretFingerprint(secret.Previous)
	}
	return reach
}
