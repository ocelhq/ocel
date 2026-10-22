package cloudflare

import (
	"context"
	"errors"
	"fmt"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/refusal"
)

func (x *Proxy) ReconcilePreviewWildcard(ctx context.Context, spec edge.PreviewWildcardSpec) (string, error) {
	wildcard := edge.PreviewWildcard(spec.BaseDomain)
	if wildcard == "" {
		return "", errors.New("forwarding previews through the Cloudflare proxy needs a base domain")
	}
	if spec.Origin == nil {
		return "", refusal.Refuse(refusal.CodeInvalid,
			"the %q edge forwards %s to an origin, and this reconcile names none: it answers no preview itself",
			Kind, wildcard)
	}
	accountID, err := x.p.readTierAccount(ctx, environment.TierPreview, fmt.Sprintf("forward %s through Cloudflare", wildcard))
	if err != nil {
		return "", err
	}
	names, err := x.p.resolveNames(ctx)
	if err != nil {
		return "", err
	}
	zone, err := x.p.resolveHostnameZone(ctx, accountID, wildcard)
	if err != nil {
		return "", err
	}
	if _, err := x.p.forwardOrigin(ctx, zone, previewForwardingOwner(names.origin), wildcard, *spec.Origin); err != nil {
		return "", err
	}
	return spec.Origin.Address, nil
}

func (x *Proxy) DestroyPreviewWildcard(ctx context.Context, baseDomain string) error {
	wildcard := edge.PreviewWildcard(baseDomain)
	if wildcard == "" {
		return nil
	}
	accountID, err := x.p.readTierAccount(ctx, environment.TierPreview, fmt.Sprintf("stop forwarding %s through Cloudflare", wildcard))
	if err != nil {
		return err
	}
	zone, err := x.p.resolveHostnameZone(ctx, accountID, wildcard)
	if err != nil {
		return err
	}
	names, err := x.p.resolveNames(ctx)
	if err != nil {
		return err
	}
	if err := x.p.dropForwardRecords(ctx, zone.zoneID, previewForwardingOwner(names.origin), wildcard); err != nil {
		return err
	}
	return x.p.releaseClientCertificate(ctx, zone, names.origin, wildcard)
}
