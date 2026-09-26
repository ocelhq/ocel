package providerkit

import (
	"context"
	"errors"
	"strings"

	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	"github.com/ocelhq/ocel/pkg/providerkit/stackrecords"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func discardCertificate(ctx context.Context, p provider.Provider, cert provider.Certificate, progress edge.Progress) error {
	if !cert.Requested || cert.ID == "" {
		return nil
	}
	return p.Certificates().Discard(ctx, cert, progress)
}

func retireCertificate(ctx context.Context, p provider.Provider, settle settlement, cert, holding provider.Certificate, progress edge.Progress) error {
	if !cert.Issued() || cert.ID == holding.ID {
		return nil
	}
	if cert.Requested {
		progress.Say("Discarding certificate " + cert.ID)
	}
	if err := discardCertificate(ctx, p, cert, progress); err != nil {
		return err
	}
	return settle.release(ctx, edge.Unwritten(cert.Written, holding.Written), progress.Say)
}

type certification struct {
	provider provider.Provider
	settle   settlement
	settled  *stackrecords.Settled
	persist  func(context.Context) error
	uses     func(string) bool
	notes    []string
}

func (c certification) certify(ctx context.Context, hostname string, progress edge.Progress) error {
	cert, err := c.provider.Certificates().Issue(ctx, provider.CertificateRequest{
		Kind:     c.settle.kind,
		Hostname: hostname,
		Current:  c.settled.Certificate,
		Progress: progress,
		Prove: func(ctx context.Context, cert provider.Certificate, records []edge.Record) (provider.Certificate, error) {
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

func (c certification) hold(ctx context.Context, cert provider.Certificate) error {
	prior := c.settled.Certificate
	c.settled.Certificate = cert
	c.settled.Supersede(prior)
	return c.persist(ctx)
}

func (c certification) discardSuperseded(ctx context.Context, progress edge.Progress) error {
	if len(c.settled.Superseded) == 0 {
		return nil
	}
	holding := c.settled.Certificate
	var kept []provider.Certificate
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
