package providerkit

import (
	"context"
	"errors"
	"strings"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type Certificate struct {
	ID        string        `json:"id,omitempty"`
	Requested bool          `json:"requested,omitempty"`
	Written   []edge.Record `json:"written,omitempty"`
	Owed      []edge.Record `json:"owed,omitempty"`
}

func (c Certificate) Held() bool { return c.ID != "" }

type Prover func(ctx context.Context, cert Certificate, records []edge.Record) (Certificate, error)

type CertificateRequest struct {
	Kind     edge.Kind
	Hostname string
	Held     Certificate
	Prove    Prover
	Report   Reporter
}

type CertificateHealth struct {
	Terminates   bool
	Status       string
	Issued       bool
	Domains      []string
	Covers       bool
	Renewal      string
	ExpiresAt    int64
	ExpiringSoon bool
}

type Certifier interface {
	Certificate(ctx context.Context, req CertificateRequest) (Certificate, error)

	InspectCertificate(ctx context.Context, kind edge.Kind, hostname string, cert Certificate) (CertificateHealth, error)

	DiscardCertificate(ctx context.Context, cert Certificate, report Reporter) error
}

func certificateFor(ctx context.Context, provider Provider, req CertificateRequest) (Certificate, error) {
	certifier, ok := provider.(Certifier)
	if !ok {
		return Certificate{}, nil
	}
	return certifier.Certificate(ctx, req)
}

func inspectCertificate(ctx context.Context, provider Provider, kind edge.Kind, hostname string, cert Certificate) (CertificateHealth, error) {
	certifier, ok := provider.(Certifier)
	if !ok {
		return CertificateHealth{}, nil
	}
	return certifier.InspectCertificate(ctx, kind, hostname, cert)
}

func discardCertificate(ctx context.Context, provider Provider, cert Certificate, report Reporter) error {
	certifier, ok := provider.(Certifier)
	if !ok || !cert.Requested || cert.ID == "" {
		return nil
	}
	return certifier.DiscardCertificate(ctx, cert, report)
}

func retireCertificate(ctx context.Context, provider Provider, settle settler, cert, holding Certificate, report Reporter) error {
	if !cert.Held() || cert.ID == holding.ID {
		return nil
	}
	if cert.Requested {
		report.Say("Discarding certificate " + cert.ID)
	}
	if err := discardCertificate(ctx, provider, cert, report); err != nil {
		return err
	}
	return settle.release(ctx, edge.Unwritten(cert.Written, holding.Written), report.Say)
}

type certification struct {
	provider Provider
	settle   settler
	settled  *Settled
	persist  func(context.Context) error
	uses     func(string) bool
	notes    []string
}

func (c certification) certify(ctx context.Context, hostname string, report Reporter) error {
	cert, err := certificateFor(ctx, c.provider, CertificateRequest{
		Kind:     c.settle.kind,
		Hostname: hostname,
		Held:     c.settled.Certificate,
		Report:   report,
		Prove: func(ctx context.Context, cert Certificate, records []edge.Record) (Certificate, error) {
			written, werr := c.settle.write(ctx, records, proveHeadline(hostname), report.Say,
				append([]string{proveNote}, c.notes...)...)
			cert.Written, cert.Owed = written.Written, written.Owed
			return cert, errors.Join(werr, c.hold(ctx, cert))
		},
	})
	if !cert.Held() {
		return err
	}
	return errors.Join(err, c.hold(ctx, cert))
}

func (c certification) hold(ctx context.Context, cert Certificate) error {
	prior := c.settled.Certificate
	c.settled.Certificate = cert
	c.settled.Supersede(prior)
	return c.persist(ctx)
}

func (c certification) discardSuperseded(ctx context.Context, report Reporter) error {
	if len(c.settled.Superseded) == 0 {
		return nil
	}
	holding := c.settled.Certificate
	var kept []Certificate
	var errs []error
	for _, cert := range c.settled.Superseded {
		if c.uses != nil && c.uses(cert.ID) {
			continue
		}
		if err := retireCertificate(ctx, c.provider, c.settle, cert, holding, report); err != nil {
			errs = append(errs, err)
			kept = append(kept, cert)
		}
	}
	c.settled.Superseded = kept
	return errors.Join(append(errs, c.persist(ctx))...)
}

const proveNote = "Leave it in place: the certificate is renewed through it."

func proveHeadline(hostname string) string {
	return "Prove you own " + strings.TrimPrefix(hostname, "*.")
}
