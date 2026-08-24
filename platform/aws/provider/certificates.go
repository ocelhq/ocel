package provider

import (
	"context"
	"fmt"
	"strings"
	"time"

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
		return issue(ctx, certifier.Issuer, req)
	}
	held, err := certifier.Issuer.Pinned(ctx, req.Hostname, pinned)
	if err != nil {
		return providerkit.Certificate{}, providerkit.Refuse(providerkit.CodeInvalid, "%s", err)
	}
	return providerkit.Certificate{ARN: held.ARN, Region: held.Region}, nil
}

func issue(ctx context.Context, issuer certs.Issuer, req providerkit.CertificateRequest) (providerkit.Certificate, error) {
	cover := []string{req.Hostname}
	say := req.Report.Say

	cert, err := recalled(ctx, issuer, req.Held, cover, say)
	if err != nil {
		return req.Held, err
	}
	if cert.Issued() {
		return req.Held, nil
	}
	if cert.ARN == "" {
		if cert, err = adoptOrRequest(ctx, issuer, cover, say); err != nil {
			return providerkit.Certificate{}, err
		}
		if cert.Adopted {
			return providerkit.Certificate{ARN: cert.ARN, Region: cert.Region}, nil
		}
	}

	settled := providerkit.Certificate{ARN: cert.ARN, Region: issuer.Region, Requested: true}
	if len(cert.Validation) == 0 {
		if cert, err = issuer.AwaitValidation(ctx, cert, say); err != nil {
			return settled, waiting(err)
		}
	}
	if settled, err = req.Prove(ctx, settled, cert.Validation); err != nil {
		return settled, err
	}
	if _, err := issuer.AwaitIssued(ctx, cert, say); err != nil {
		return settled, waiting(err)
	}
	return settled, nil
}

func recalled(ctx context.Context, issuer certs.Issuer, recorded providerkit.Certificate, cover []string, say func(string)) (certs.Certificate, error) {
	if recorded.ARN == "" || (recorded.Region != "" && recorded.Region != issuer.Region) {
		return certs.Certificate{}, nil
	}
	adopted := !recorded.Requested
	live, err := issuer.Describe(ctx, certs.Certificate{ARN: recorded.ARN, Region: issuer.Region, Adopted: adopted})
	if err != nil && !certs.Gone(err) {
		return certs.Certificate{}, err
	}
	if err == nil && live.CoversAll(cover) && (live.Issued() || recorded.Requested) {
		live.Adopted = adopted
		return live, nil
	}
	say(fmt.Sprintf("Certificate %s no longer answers for %s in %s; settling one that does",
		recorded.ARN, strings.Join(cover, ", "), issuer.Region))
	return certs.Certificate{}, nil
}

func adoptOrRequest(ctx context.Context, issuer certs.Issuer, cover []string, say func(string)) (certs.Certificate, error) {
	found, err := issuer.Existing(ctx, cover)
	if err != nil {
		return certs.Certificate{}, err
	}
	if found.ARN != "" {
		say(fmt.Sprintf("Reusing certificate %s in %s: it already covers %s", found.ARN, issuer.Region, strings.Join(cover, ", ")))
		return found, nil
	}
	say(fmt.Sprintf("Requesting a certificate for %s in %s", strings.Join(cover, ", "), issuer.Region))
	return issuer.Request(ctx, cover)
}

func waiting(err error) error {
	if !certs.Pending(err) {
		return err
	}
	return providerkit.Refuse(providerkit.CodeNotReady, "%s", err)
}

func (p *Provider) InspectCertificate(ctx context.Context, kind edge.Kind, hostname string, cert providerkit.Certificate) (providerkit.CertificateHealth, error) {
	registry := p.edges()
	front, err := registry.Open(kind)
	if err != nil {
		return providerkit.CertificateHealth{}, err
	}
	certifier := registry.Certifier(front, certs.Deps{AWS: p.aws})
	if !certifier.Issues() {
		return providerkit.CertificateHealth{}, nil
	}
	health := providerkit.CertificateHealth{Terminates: true}
	arn := certifier.Wants(certs.Certificate{ARN: cert.ARN}, hostname)
	if arn == "" {
		return health, nil
	}
	described, err := certifier.Issuer.Describe(ctx, certs.Certificate{ARN: arn, Region: certifier.Issuer.Region})
	if err != nil {
		if certs.Gone(err) {
			return health, nil
		}
		return health, err
	}
	health.Status = described.Status
	health.Issued = described.Issued()
	health.Domains = described.Domains
	health.Covers = described.Covers(hostname)
	health.Renewal = described.Renewal
	if !described.NotAfter.IsZero() {
		health.ExpiresAt = described.NotAfter.Unix()
		health.ExpiringSoon = described.ExpiringSoon(time.Now())
	}
	return health, nil
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
