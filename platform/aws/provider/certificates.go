package provider

import (
	"context"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/platform/aws/provider/certs"
	"github.com/ocelhq/ocel/platform/aws/provider/edges"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func (p *Provider) Certificate(ctx context.Context, req providerkit.CertificateRequest) (providerkit.Certificate, error) {
	certifier, err := p.certifier(req.Kind, req.Hostname, req.Report)
	if err != nil || !certifier.Issues() {
		return providerkit.Certificate{}, err
	}
	pinned := certifier.PinFor(req.Hostname)
	if pinned == "" {
		// TODO(#390): nothing here requests a certificate yet, so a hostname without a pin
		// has none to be served with; refuse before the edge attaches an empty one.
		return providerkit.Certificate{}, providerkit.Refuse(providerkit.CodeNotReady,
			"the %s edge terminates TLS for %s itself, and ocel issues no certificate yet: pin one already issued in %s that covers %s under `certificates` in this provider's options, then run this again",
			req.Kind, req.Hostname, certifier.Issuer.Region, req.Hostname)
	}
	held, err := certifier.Issuer.Pinned(ctx, req.Hostname, pinned)
	if err != nil {
		return providerkit.Certificate{}, providerkit.Refuse(providerkit.CodeInvalid, "%s", err)
	}
	return providerkit.Certificate{ARN: held.ARN, Region: held.Region}, nil
}

func (p *Provider) DiscardCertificate(ctx context.Context, cert providerkit.Certificate, report providerkit.Reporter) error {
	if !cert.Requested || cert.ARN == "" {
		return nil
	}
	held := certs.Certificate{ARN: cert.ARN, Region: cert.Region}
	return certs.DiscardIssuerFor(held, certs.Deps{AWS: p.aws}).Discard(ctx, held, report.Say)
}

func (p *Provider) certifier(kind edge.Kind, hostname string, report providerkit.Reporter) (certs.Certifier, error) {
	registry := p.edges()
	front, err := registry.Open(kind)
	if err != nil {
		return certs.Certifier{}, err
	}
	certifier := registry.Certifier(front, certs.Deps{AWS: p.aws})
	if note := edges.IgnoredPinNote(front, certifier, hostname); note != "" {
		report.Detail(note)
	}
	return certifier, nil
}
