package gcp

import (
	"context"
	"net/url"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/gcp/provider/bucket"
)

const (
	storeSigningRegion = "auto"

	imageEndpointMaxRequestBytes = 32 << 20

	cloudRunThrottleMessage = "no available instance"
)

func (p *Provider) ProgramEdge(ctx context.Context, req provider.EdgeProgramRequest) (edge.Program, error) {
	c, err := p.openClients(ctx)
	if err != nil {
		return edge.Program{}, err
	}
	adopted, credentials, err := requireAdoptedEdge(ctx, c, p.KeyValues(), req.Tier, req.Kind)
	if err != nil {
		return edge.Program{}, err
	}
	if adopted.ClientCertificate.ID == "" {
		return edge.Program{}, refusal.Refuse(refusal.CodeNotReady,
			"the %s edge's worker reaches GCP only through its client certificate, and the bootstrap adopted none: run `%s` again",
			req.Kind, provider.BootstrapCommand(req.Tier))
	}
	if !credentials.hasAssetStoreKey() {
		return edge.Program{}, notBootstrapped(req.Tier, req.Kind, "no asset-store credential")
	}
	return edge.Program{
		Origin: edge.OriginReach{
			Reach: edge.Reach{Kind: edge.ReachClientCertificate, Certificate: adopted.ClientCertificate.ID},
			// TODO: serve ISR entries from the store at the edge (HoldsEntries) once the edge
			// reads with a per-project identity. The asset-store key belongs to one account per
			// tier, so widening it to cache/ would let each project's worker read every other
			// project's entries, and entries live at cache/<project>/<app>/<env>/..., which
			// EntriesPrefix plus the record's key cannot name.
			Store: &edge.OriginStore{
				URL:             bucket.GoogleStorage + "/" + url.PathEscape(c.Bucket(req.Tier)),
				Prefix:          provider.StoreAssets + "/",
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
