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

func (c Certificate) Issued() bool { return c.ID != "" }

type Prover func(ctx context.Context, cert Certificate, records []edge.Record) (Certificate, error)

type CertificateRequest struct {
	Kind     edge.Kind
	Hostname string
	Current  Certificate
	Prove    Prover
	Progress Progress
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

type Certificates interface {
	Issue(ctx context.Context, req CertificateRequest) (Certificate, error)

	Inspect(ctx context.Context, kind edge.Kind, hostname string, cert Certificate) (CertificateHealth, error)

	Discard(ctx context.Context, cert Certificate, progress Progress) error
}

func discardCertificate(ctx context.Context, provider Provider, cert Certificate, progress Progress) error {
	if !cert.Requested || cert.ID == "" {
		return nil
	}
	return provider.Certificates().Discard(ctx, cert, progress)
}

func retireCertificate(ctx context.Context, provider Provider, settle settler, cert, holding Certificate, progress Progress) error {
	if !cert.Issued() || cert.ID == holding.ID {
		return nil
	}
	if cert.Requested {
		progress.Say("Discarding certificate " + cert.ID)
	}
	if err := discardCertificate(ctx, provider, cert, progress); err != nil {
		return err
	}
	return settle.release(ctx, edge.Unwritten(cert.Written, holding.Written), progress.Say)
}

type certification struct {
	provider Provider
	settle   settler
	settled  *Settled
	persist  func(context.Context) error
	uses     func(string) bool
	notes    []string
}

func (c certification) certify(ctx context.Context, hostname string, progress Progress) error {
	cert, err := c.provider.Certificates().Issue(ctx, CertificateRequest{
		Kind:     c.settle.kind,
		Hostname: hostname,
		Current:  c.settled.Certificate,
		Progress: progress,
		Prove: func(ctx context.Context, cert Certificate, records []edge.Record) (Certificate, error) {
			written, werr := c.settle.write(ctx, records, proveHeadline(hostname), progress.Say,
				append([]string{proveNote}, c.notes...)...)
			cert.Written, cert.Owed = written.Written, written.Owed
			return cert, errors.Join(werr, c.hold(ctx, cert))
		},
	})
	if !cert.Issued() {
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

func (c certification) discardSuperseded(ctx context.Context, progress Progress) error {
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
		if err := retireCertificate(ctx, c.provider, c.settle, cert, holding, progress); err != nil {
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
