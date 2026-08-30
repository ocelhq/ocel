package vps

import (
	"context"
	"time"

	"github.com/ocelhq/ocel/pkg/providerkit"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
	"github.com/ocelhq/ocel/platform/vps/provider/certs"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
)

func (p *Provider) Certificate(ctx context.Context, req providerkit.CertificateRequest) (providerkit.Certificate, error) {
	path := p.host.PinFor(req.Hostname)
	if path == "" {
		if err := p.host.CertificateTrouble(ctx, req.Hostname); err != nil {
			return providerkit.Certificate{}, err
		}
		return providerkit.Certificate{ID: certs.ProxyHandle(req.Hostname)}, nil
	}
	leaf, err := p.pinnedLeaf(ctx, path)
	if err != nil {
		return providerkit.Certificate{}, err
	}
	if err := certs.Verify(path, req.Hostname, leaf, time.Now()); err != nil {
		return providerkit.Certificate{}, err
	}
	return providerkit.Certificate{ID: certs.PinHandle(path)}, nil
}

func (p *Provider) InspectCertificate(ctx context.Context, _ edge.Kind, hostname string, cert providerkit.Certificate) (providerkit.CertificateHealth, error) {
	health := providerkit.CertificateHealth{Terminates: true}
	if !cert.Held() {
		return health, nil
	}
	health.Renewal = certs.Renewal(cert.ID)
	if path, pinned := certs.Pinned(cert.ID); pinned {
		return p.pinnedHealth(ctx, path, hostname, health)
	}
	served, ok := certs.Serving(cert.ID)
	if !ok {
		return providerkit.CertificateHealth{}, providerkit.Refuse(providerkit.CodeInvalid,
			"%s is bound to %q, and a certificate on this box is either %s<hostname> the proxy obtained or %s<path> you pinned: reading a handle this box never minted as one it did would report on whatever the proxy happens to serve",
			hostname, cert.ID, certs.ProxyScheme, certs.PinScheme)
	}
	return p.servedHealth(ctx, served, hostname, health)
}

func (p *Provider) DiscardCertificate(context.Context, providerkit.Certificate, providerkit.Reporter) error {
	return nil
}

func (p *Provider) pinnedLeaf(ctx context.Context, path string) (certs.Leaf, error) {
	block, err := p.host.PinnedCertificate(ctx, path)
	if err != nil {
		return certs.Leaf{}, err
	}
	return certs.Parse(host.PinCertificate(path), block)
}

func (p *Provider) pinnedHealth(ctx context.Context, path, hostname string, health providerkit.CertificateHealth) (providerkit.CertificateHealth, error) {
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

func (p *Provider) servedHealth(ctx context.Context, served, hostname string, health providerkit.CertificateHealth) (providerkit.CertificateHealth, error) {
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

var _ providerkit.Certifier = (*Provider)(nil)
