package providerserver

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

func retireCertificate(ctx context.Context, p provider.Provider, cutover dnsCutover, cert, holding provider.Certificate, progress edge.Progress) error {
	if !cert.Issued() || cert.ID == holding.ID {
		return nil
	}
	if cert.Requested {
		progress.Say("Discarding certificate " + cert.ID)
	}
	if err := discardCertificate(ctx, p, cert, progress); err != nil {
		return err
	}
	return cutover.release(ctx, edge.Unwritten(cert.Written, holding.Written), progress.Say)
}

type hostCertificates struct {
	provider  provider.Provider
	cutover   dnsCutover
	hostState *stackrecords.HostnameState
	persist   func(context.Context) error
	uses      func(string) bool
	notes     []string
}

func (c hostCertificates) certify(ctx context.Context, hostname string, progress edge.Progress) error {
	cert, err := c.provider.Certificates().Issue(ctx, provider.CertificateRequest{
		Kind:     c.cutover.kind,
		Hostname: hostname,
		Current:  c.hostState.Certificate,
		Progress: progress,
		Prove: func(ctx context.Context, cert provider.Certificate, records []edge.Record) (provider.Certificate, error) {
			written, werr := c.cutover.write(ctx, records, proveHeadline(hostname), progress.Say,
				append([]string{proveNote}, c.notes...)...)
			cert.Written, cert.Manual = written.Written, written.Manual
			return cert, errors.Join(werr, c.adoptCertificate(ctx, cert))
		},
	})
	if !cert.Issued() {
		return err
	}
	return errors.Join(err, c.adoptCertificate(ctx, cert))
}

func (c hostCertificates) adoptCertificate(ctx context.Context, cert provider.Certificate) error {
	prior := c.hostState.Certificate
	c.hostState.Certificate = cert
	c.hostState.Supersede(prior)
	return c.persist(ctx)
}

func (c hostCertificates) discardSuperseded(ctx context.Context, progress edge.Progress) error {
	if len(c.hostState.Superseded) == 0 {
		return nil
	}
	holding := c.hostState.Certificate
	var kept []provider.Certificate
	var errs []error
	for _, cert := range c.hostState.Superseded {
		if c.uses != nil && c.uses(cert.ID) {
			continue
		}
		if err := retireCertificate(ctx, c.provider, c.cutover, cert, holding, progress); err != nil {
			errs = append(errs, err)
			kept = append(kept, cert)
		}
	}
	c.hostState.Superseded = kept
	return errors.Join(append(errs, c.persist(ctx))...)
}

const proveNote = "Leave it in place: the certificate is renewed through it."

func proveHeadline(hostname string) string {
	return "Prove you own " + strings.TrimPrefix(hostname, "*.")
}
