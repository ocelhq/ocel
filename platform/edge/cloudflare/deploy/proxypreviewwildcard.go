package cloudflare

import (
	"context"
	"errors"
	"fmt"

	"github.com/ocelhq/ocel/pkg/edge"
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
	accountID, err := requireAccountID(fmt.Sprintf("forward %s through Cloudflare", wildcard))
	if err != nil {
		return "", err
	}
	if _, err := x.p.forwardOrigin(ctx, accountID, edge.PreviewEntryOwner, wildcard, *spec.Origin); err != nil {
		return "", err
	}
	return spec.Origin.Address, nil
}

func (x *Proxy) DestroyPreviewWildcard(ctx context.Context, baseDomain string) error {
	wildcard := edge.PreviewWildcard(baseDomain)
	if wildcard == "" {
		return nil
	}
	accountID, err := requireAccountID(fmt.Sprintf("stop forwarding %s through Cloudflare", wildcard))
	if err != nil {
		return err
	}
	zoneID, _, err := x.p.resolveZone(ctx, accountID, baseDomain)
	if err != nil {
		return err
	}
	return x.p.dropForwardRecords(ctx, zoneID, edge.PreviewEntryOwner, wildcard)
}
