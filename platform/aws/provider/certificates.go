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

type certificates struct{ *Provider }

func (p certificates) Issue(ctx context.Context, req providerkit.CertificateRequest) (providerkit.Certificate, error) {
	certificates, err := p.certificatesFor(req.Kind, req.Hostname, req.Progress)
	if err != nil || !certificates.Issues() {
		return providerkit.Certificate{}, err
	}
	pinned := certificates.PinFor(req.Hostname)
	if pinned == "" {
		return issue(ctx, certificates.ACM, req)
	}
	held, err := certificates.ACM.Pinned(ctx, req.Hostname, pinned)
	if err != nil {
		return providerkit.Certificate{}, providerkit.Refuse(providerkit.CodeInvalid, "%s", err)
	}
	return providerkit.Certificate{ID: held.ARN}, nil
}

func issue(ctx context.Context, acm certs.ACM, req providerkit.CertificateRequest) (providerkit.Certificate, error) {
	cover := []string{req.Hostname}
	say := req.Progress.Say

	cert, err := recalled(ctx, acm, req.Current, cover, say)
	if err != nil {
		return req.Current, err
	}
	if cert.Issued() {
		return req.Current, nil
	}
	if cert.ARN == "" {
		if cert, err = adoptOrRequest(ctx, acm, cover, say); err != nil {
			return providerkit.Certificate{}, err
		}
		if cert.Adopted {
			return providerkit.Certificate{ID: cert.ARN}, nil
		}
	}

	settled := providerkit.Certificate{ID: cert.ARN, Requested: true}
	if len(cert.Validation) == 0 {
		if cert, err = acm.AwaitValidation(ctx, cert, say); err != nil {
			return settled, waiting(err)
		}
	}
	if settled, err = req.Prove(ctx, settled, cert.Validation); err != nil {
		return settled, err
	}
	if _, err := acm.AwaitIssued(ctx, cert, say); err != nil {
		return settled, waiting(err)
	}
	return settled, nil
}

func recalled(ctx context.Context, acm certs.ACM, recorded providerkit.Certificate, cover []string, say func(string)) (certs.Certificate, error) {
	region := certs.RegionOfARN(recorded.ID)
	if recorded.ID == "" || (region != "" && region != acm.Region) {
		return certs.Certificate{}, nil
	}
	adopted := !recorded.Requested
	live, err := acm.Describe(ctx, certs.Certificate{ARN: recorded.ID, Region: acm.Region, Adopted: adopted})
	if err != nil && !certs.Gone(err) {
		return certs.Certificate{}, err
	}
	if err == nil && live.CoversAll(cover) && (live.Issued() || recorded.Requested) {
		live.Adopted = adopted
		return live, nil
	}
	say(fmt.Sprintf("Certificate %s no longer answers for %s in %s; settling one that does",
		recorded.ID, strings.Join(cover, ", "), acm.Region))
	return certs.Certificate{}, nil
}

func adoptOrRequest(ctx context.Context, acm certs.ACM, cover []string, say func(string)) (certs.Certificate, error) {
	found, err := acm.Existing(ctx, cover)
	if err != nil {
		return certs.Certificate{}, err
	}
	if found.ARN != "" {
		say(fmt.Sprintf("Reusing certificate %s in %s: it already covers %s", found.ARN, acm.Region, strings.Join(cover, ", ")))
		return found, nil
	}
	say(fmt.Sprintf("Requesting a certificate for %s in %s", strings.Join(cover, ", "), acm.Region))
	return acm.Request(ctx, cover)
}

func waiting(err error) error {
	if !certs.Pending(err) {
		return err
	}
	return providerkit.Pending(providerkit.Refuse(providerkit.CodeNotReady, "%s", err))
}

func (p certificates) Inspect(ctx context.Context, kind edge.Kind, hostname string, cert providerkit.Certificate) (providerkit.CertificateHealth, error) {
	registry := p.edges()
	front, err := registry.Open(kind)
	if err != nil {
		return providerkit.CertificateHealth{}, err
	}
	certificates := registry.Certificates(front, certs.Deps{AWS: p.aws})
	if !certificates.Issues() {
		return providerkit.CertificateHealth{}, nil
	}
	health := providerkit.CertificateHealth{Terminates: true}
	arn := certificates.Wants(certs.Certificate{ARN: cert.ID}, hostname)
	if arn == "" {
		return health, nil
	}
	described, err := certificates.ACM.Describe(ctx, certs.Certificate{ARN: arn, Region: certificates.ACM.Region})
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

func (p certificates) Discard(ctx context.Context, cert providerkit.Certificate, progress providerkit.Progress) error {
	if !cert.Requested || cert.ID == "" {
		return nil
	}
	held := certs.Certificate{ARN: cert.ID, Region: certs.RegionOfARN(cert.ID)}
	return certs.DiscardACMFor(held, certs.Deps{AWS: p.aws}).Discard(ctx, held, progress.Say)
}

func (p *Provider) certificatesFor(kind edge.Kind, hostname string, progress providerkit.Progress) (certs.Certificates, error) {
	registry := p.edges()
	front, err := registry.Open(kind)
	if err != nil {
		return certs.Certificates{}, err
	}
	certificates := registry.Certificates(front, certs.Deps{AWS: p.aws})
	if note := edges.IgnoredPinNote(front, certificates, hostname); note != "" {
		progress.Detail(note)
	}
	return certificates, nil
}
