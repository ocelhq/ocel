package fake

import (
	"context"
	"slices"
	"strconv"

	"github.com/ocelhq/ocel/pkg/providerkit"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func (p *Provider) Pin(hostname, certificate string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.pins == nil {
		p.pins = map[string]string{}
	}
	p.pins[hostname] = certificate
}

func (p *Provider) RefuseCertificates(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.certRefusal = err
}

func (p *Provider) IssueCertificates(validation ...edge.Record) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.issue = validation
}

func (p *Provider) RotateCertificates() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.rotation++
}

func (p *Provider) StallAfterProving(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.pending = err
}

func (p *Provider) RefuseDiscardingAServingCertificate(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.discardHeld = err
}

func (p *Provider) Discarded() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.discarded)
}

type certificates struct{ *Provider }

func (p certificates) Issue(ctx context.Context, req providerkit.CertificateRequest) (providerkit.Certificate, error) {
	p.mu.Lock()
	refusal, pinned, validation := p.certRefusal, p.pins[req.Hostname], slices.Clone(p.issue)
	rotation, pending := p.rotation, p.pending
	p.mu.Unlock()

	if refusal != nil {
		return providerkit.Certificate{}, refusal
	}
	if pinned != "" {
		return providerkit.Certificate{ID: pinned}, nil
	}
	if validation == nil {
		return providerkit.Certificate{}, nil
	}
	cert := providerkit.Certificate{ID: issuedFor(req.Hostname, rotation), Requested: true}
	if req.Current.Requested && req.Current.ID == cert.ID {
		return req.Current, nil
	}
	settled, err := req.Prove(ctx, cert, validation)
	if err != nil {
		return settled, err
	}
	return settled, pending
}

func issuedFor(hostname string, rotation int) string {
	id := "issued-for-" + hostname
	if rotation == 0 {
		return id
	}
	return id + "-" + strconv.Itoa(rotation)
}

func (p *Provider) ReportCertificate(health providerkit.CertificateHealth) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.health = &health
}

func (p certificates) Inspect(_ context.Context, _ edge.Kind, hostname string, cert providerkit.Certificate) (providerkit.CertificateHealth, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.health != nil {
		return *p.health, nil
	}
	if p.pins == nil && p.issue == nil {
		return providerkit.CertificateHealth{}, nil
	}
	health := providerkit.CertificateHealth{Terminates: true}
	if cert.ID == "" {
		return health, nil
	}
	health.Status, health.Issued = "issued", true
	health.Domains, health.Covers = []string{hostname}, true
	return health, nil
}

func (p certificates) Discard(_ context.Context, cert providerkit.Certificate, _ providerkit.Progress) error {
	p.mu.Lock()
	refusal := p.discardHeld
	p.mu.Unlock()
	if refusal != nil && p.edges.serving(cert.ID) {
		return refusal
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.discarded = append(p.discarded, cert.ID)
	return nil
}
