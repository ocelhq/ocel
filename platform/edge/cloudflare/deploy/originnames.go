package cloudflare

import (
	"context"
	"errors"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
)

type originNames struct {
	namespace string
	origin    OriginScope
}

func (p *cloudflare) resolveNames(ctx context.Context) (originNames, error) {
	if p.namespace == "" {
		return originNames{}, errors.New("the cloudflare edge was opened without a bootstrap namespace, so it cannot name the workers and certificates this origin keeps in the account")
	}
	origin, err := p.originScope(ctx)
	if err != nil {
		return originNames{}, err
	}
	return originNames{namespace: p.namespace, origin: origin}, nil
}

func (n originNames) accountName(what string, tier environment.Tier, stem string) (string, error) {
	if err := refuseUnknownTier(what, tier); err != nil {
		return "", err
	}
	return naming.OcelName(n.namespace, tier, n.origin.tag(), stem), nil
}
