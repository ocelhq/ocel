package vps

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	"github.com/ocelhq/ocel/pkg/providerkit/refusal"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
	"github.com/ocelhq/ocel/platform/vps/provider/certs"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy/caddy"
)

type certificates struct{ *Provider }

func (p certificates) Issue(ctx context.Context, req provider.CertificateRequest) (provider.Certificate, error) {
	path := p.host.PinFor(req.Hostname)
	if path == "" {
		if strings.HasPrefix(req.Hostname, "*.") {
			return provider.Certificate{}, nil
		}
		current, err := p.host.FrontProxy().Certificate(ctx, req.Hostname)
		if current.Trouble != nil && refused(current.Trouble) {
			return provider.Certificate{}, current.Trouble
		}
		if err != nil && req.Progress != nil {
			req.Progress.Say("could not read the certificate the proxy has for " + req.Hostname + ": " + err.Error())
		}
		return provider.Certificate{ID: certs.ProxyHandle(req.Hostname)}, nil
	}
	leaf, err := p.pinnedLeaf(ctx, path)
	if err != nil {
		return provider.Certificate{}, err
	}
	if err := certs.Verify(path, req.Hostname, leaf, time.Now()); err != nil {
		return provider.Certificate{}, err
	}
	return provider.Certificate{ID: certs.PinHandle(path)}, nil
}

func refused(err error) bool {
	var rejection refusal.Refusal
	return errors.As(err, &rejection) && rejection.Code == refusal.CodeBusy
}

func (p certificates) Inspect(ctx context.Context, _ edge.Kind, hostname string, cert provider.Certificate) (provider.CertificateHealth, error) {
	health := provider.CertificateHealth{Terminates: true}
	if !cert.Issued() {
		return health, nil
	}
	health.Renewal = certs.Renewal(cert.ID)
	if !p.host.FrontProxy().Guarantees().OwnsPorts {
		health.Renewal = certs.AdoptedRenewal
	}
	if path, pinned := certs.Pinned(cert.ID); pinned {
		return p.pinnedHealth(ctx, path, hostname, health)
	}
	served, ok := certs.Serving(cert.ID)
	if !ok {
		return provider.CertificateHealth{}, refusal.Refuse(refusal.CodeInvalid,
			"%s is bound to %q, which is neither %s<hostname> nor %s<path>",
			hostname, cert.ID, certs.ProxyScheme, certs.PinScheme)
	}
	return p.servedHealth(ctx, served, hostname, health)
}

func (p certificates) Discard(context.Context, provider.Certificate, edge.Progress) error {
	return nil
}

func (p *Provider) pinnedLeaf(ctx context.Context, path string) (certs.Leaf, error) {
	block, err := p.host.PinnedCertificate(ctx, path)
	if err != nil {
		return certs.Leaf{}, err
	}
	return certs.Parse(caddy.PinCertificate(path), block)
}

func (p *Provider) pinnedHealth(ctx context.Context, path, hostname string, health provider.CertificateHealth) (provider.CertificateHealth, error) {
	leaf, err := p.pinnedLeaf(ctx, path)
	if err != nil {
		return health, err
	}
	now := time.Now()
	health.Issued = !leaf.Expired(now)
	health.Domains = leaf.Domains
	health.Covers = leaf.Covers(hostname)
	health.ExpiresAt = leaf.ExpiresAt()
	health.ExpiringSoon = leaf.ExpiringSoon(now)
	health.Status = pinnedStatus(leaf, hostname, now)
	return health, nil
}

func pinnedStatus(leaf certs.Leaf, hostname string, now time.Time) string {
	switch {
	case leaf.Expired(now):
		return "EXPIRED"
	case !leaf.Covers(hostname):
		return "DOES_NOT_COVER"
	default:
		return "PINNED"
	}
}

func (p *Provider) servedHealth(ctx context.Context, served, hostname string, health provider.CertificateHealth) (provider.CertificateHealth, error) {
	block, err := p.host.ServedCertificate(ctx, served)
	if err != nil {
		return health, err
	}
	if len(block) == 0 {
		health.Status = "PENDING"
		return health, nil
	}
	leaf, err := certs.Parse("the certificate the proxy serves for "+served, block)
	if err != nil {
		return health, err
	}
	expired := leaf.Expired(time.Now())
	health.Issued = !expired
	health.Status = "SERVING"
	if expired {
		health.Status = "EXPIRED"
	}
	health.Domains = leaf.Domains
	health.Covers = leaf.Covers(hostname)
	return health, nil
}
