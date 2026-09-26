package gcp

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	certmanager "google.golang.org/api/certificatemanager/v1"
	"google.golang.org/api/googleapi"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	"github.com/ocelhq/ocel/pkg/providerkit/refusal"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const (
	certificateActive       = "ACTIVE"
	certificateProvisioning = "PROVISIONING"
	certificateFailed       = "FAILED"
)

const certificateRenewal = "Certificate Manager renews it while the authorization record exists"

const certificateExpiry = 30 * 24 * time.Hour

var issuance = patience{attempts: 100, ceiling: 15 * time.Second}

const maxCertificateName = 63

func authorizationID(hostname string) string { return certificateName(hostname, "auth") }

func certificateName(hostname string, role ...string) string {
	segments := []naming.Segment{naming.Fixed("ocel"), naming.Compressible(naming.SanitizeHost(hostname))}
	for _, each := range role {
		segments = append(segments, naming.Fixed(each))
	}
	return naming.Fit(maxCertificateName, naming.WordSeparator, segments...)
}

func authorizedDomain(hostname string) string { return strings.TrimPrefix(hostname, "*.") }

type certificates struct{ *Provider }

func (p certificates) Issue(ctx context.Context, req provider.CertificateRequest) (provider.Certificate, error) {
	clients, err := p.openClients(ctx)
	if err != nil {
		return provider.Certificate{}, err
	}
	name := clients.certificatesGlobal() + "/certificates/" + certificateName(req.Hostname)
	cert := provider.Certificate{ID: name, Requested: true, Written: req.Current.Written, Manual: req.Current.Manual}

	authorization, err := p.authorized(ctx, clients, req)
	if err != nil {
		return provider.Certificate{}, err
	}
	cert, err = req.Prove(ctx, cert, []edge.Record{{
		Name:  strings.TrimSuffix(authorization.DnsResourceRecord.Name, "."),
		Type:  edge.RecordTypeCNAME,
		Value: authorization.DnsResourceRecord.Data,
	}})
	if err != nil {
		return cert, err
	}
	if err := p.certified(ctx, clients, req, name, authorization.Name); err != nil {
		return cert, err
	}
	return cert, nil
}

func (p *Provider) authorized(
	ctx context.Context,
	clients *clients,
	req provider.CertificateRequest,
) (*certmanager.DnsAuthorization, error) {
	certificates, err := clients.Certificates()
	if err != nil {
		return nil, err
	}
	id := authorizationID(req.Hostname)
	name := clients.certificatesGlobal() + "/dnsAuthorizations/" + id
	authorization, err := attempted(ctx, func(call ...googleapi.CallOption) (*certmanager.DnsAuthorization, error) {
		return certificates.Projects.Locations.DnsAuthorizations.Get(name).Context(ctx).Do(call...)
	})
	switch {
	case err == nil:
		return authorization, nil
	case !absent(err):
		return nil, fmt.Errorf("read the dns authorization for %s: %w", req.Hostname, err)
	}
	if req.Progress != nil {
		req.Progress.Say("Asking Certificate Manager to authorize " + authorizedDomain(req.Hostname))
	}
	err = p.awaitCertificates(ctx, certificates, "authorize "+req.Hostname,
		func(call ...googleapi.CallOption) (*certmanager.Operation, error) {
			return certificates.Projects.Locations.DnsAuthorizations.
				Create(clients.certificatesGlobal(), &certmanager.DnsAuthorization{Domain: authorizedDomain(req.Hostname)}).
				DnsAuthorizationId(id).Context(ctx).Do(call...)
		})
	if err != nil {
		return nil, err
	}
	authorization, err = attempted(ctx, func(call ...googleapi.CallOption) (*certmanager.DnsAuthorization, error) {
		return certificates.Projects.Locations.DnsAuthorizations.Get(name).Context(ctx).Do(call...)
	})
	if err != nil {
		return nil, fmt.Errorf("read the dns authorization for %s: %w", req.Hostname, err)
	}
	if authorization.DnsResourceRecord == nil {
		return nil, refusal.Refuse(refusal.CodeNotReady,
			"the dns authorization for %s names no record to write, and ownership of a domain is proved by writing the one Certificate Manager names",
			req.Hostname)
	}
	return authorization, nil
}

func (p *Provider) certificates(ctx context.Context) (*clients, *certmanager.Service, error) {
	clients, err := p.openClients(ctx)
	if err != nil {
		return nil, nil, err
	}
	certificates, err := clients.Certificates()
	if err != nil {
		return nil, nil, err
	}
	return clients, certificates, nil
}

func (p *Provider) certified(
	ctx context.Context,
	clients *clients,
	req provider.CertificateRequest,
	name, authorization string,
) error {
	certificates, err := clients.Certificates()
	if err != nil {
		return err
	}
	_, err = attempted(ctx, func(call ...googleapi.CallOption) (*certmanager.Certificate, error) {
		return certificates.Projects.Locations.Certificates.Get(name).Context(ctx).Do(call...)
	})
	switch {
	case err == nil:
	case !absent(err):
		return fmt.Errorf("read the certificate for %s: %w", req.Hostname, err)
	default:
		if req.Progress != nil {
			req.Progress.Say("Asking Certificate Manager for a certificate covering " + req.Hostname)
		}
		err = p.awaitCertificates(ctx, certificates, "certify "+req.Hostname,
			func(call ...googleapi.CallOption) (*certmanager.Operation, error) {
				return certificates.Projects.Locations.Certificates.
					Create(clients.certificatesGlobal(), &certmanager.Certificate{
						Managed: &certmanager.ManagedCertificate{
							Domains:           []string{req.Hostname},
							DnsAuthorizations: []string{authorization},
						},
					}).
					CertificateId(certificateName(req.Hostname)).Context(ctx).Do(call...)
			})
		if err != nil {
			return err
		}
	}
	return p.issued(ctx, certificates, req, name)
}

func (p *Provider) issued(
	ctx context.Context,
	certificates *certmanager.Service,
	req provider.CertificateRequest,
	name string,
) error {
	certificate, err := waiting(ctx, issuance, "Certificate Manager to issue a certificate for "+req.Hostname,
		func() (*certmanager.Certificate, error) {
			return attempted(ctx, func(call ...googleapi.CallOption) (*certmanager.Certificate, error) {
				return certificates.Projects.Locations.Certificates.Get(name).Context(ctx).Do(call...)
			})
		},
		func(cert *certmanager.Certificate) bool {
			return cert != nil && cert.Managed != nil && cert.Managed.State != certificateProvisioning
		})
	if code, _ := provider.RefusedCode(err); code == refusal.CodeNotReady {
		return provider.Resumable(err)
	}
	if err != nil {
		return err
	}
	if certificate.Managed.State == certificateActive {
		return nil
	}
	return refusal.Refuse(refusal.CodeNotReady,
		"Certificate Manager gave up on the certificate for %s: %s.\n"+
			"The authorization record has to resolve from the public internet before Google will issue: check it, then bind the hostname again",
		req.Hostname, provisioningIssue(certificate))
}

func provisioningIssue(cert *certmanager.Certificate) string {
	if cert.Managed.ProvisioningIssue == nil {
		return cert.Managed.State
	}
	if details := cert.Managed.ProvisioningIssue.Details; details != "" {
		return details
	}
	return cert.Managed.ProvisioningIssue.Reason
}

func (p certificates) Inspect(
	ctx context.Context,
	_ edge.Kind,
	hostname string,
	cert provider.Certificate,
) (provider.CertificateHealth, error) {
	if !cert.Issued() {
		return provider.CertificateHealth{}, nil
	}
	_, certificates, err := p.certificates(ctx)
	if err != nil {
		return provider.CertificateHealth{}, err
	}
	current, err := attempted(ctx, func(call ...googleapi.CallOption) (*certmanager.Certificate, error) {
		return certificates.Projects.Locations.Certificates.Get(cert.ID).Context(ctx).Do(call...)
	})
	if absent(err) {
		return provider.CertificateHealth{Terminates: true, Status: "gone", Renewal: certificateRenewal}, nil
	}
	if err != nil {
		return provider.CertificateHealth{}, fmt.Errorf("read the certificate %s: %w", cert.ID, err)
	}
	health := provider.CertificateHealth{
		Terminates: true,
		Renewal:    certificateRenewal,
		Domains:    covered(current),
	}
	health.Status = certificateProvisioning
	if current.Managed != nil {
		health.Status = current.Managed.State
	}
	health.Issued = health.Status == certificateActive
	health.Covers = slices.Contains(health.Domains, hostname)
	if lapses, err := time.Parse(time.RFC3339, current.ExpireTime); err == nil {
		health.ExpiresAt = lapses.Unix()
		health.ExpiringSoon = time.Until(lapses) < certificateExpiry
	}
	return health, nil
}

func covered(cert *certmanager.Certificate) []string {
	if len(cert.SanDnsnames) > 0 {
		return slices.Clone(cert.SanDnsnames)
	}
	if cert.Managed != nil {
		return slices.Clone(cert.Managed.Domains)
	}
	return nil
}

func (p *Provider) Entered(ctx context.Context, certificateMap string) ([]string, error) {
	clients, certificates, err := p.certificates(ctx)
	if err != nil {
		return nil, err
	}
	parent := clients.certificatesGlobal() + "/certificateMaps/" + certificateMap
	var bound []string
	for page := ""; ; {
		entries, err := attempted(ctx, func(call ...googleapi.CallOption) (*certmanager.ListCertificateMapEntriesResponse, error) {
			return certificates.Projects.Locations.CertificateMaps.CertificateMapEntries.
				List(parent).PageToken(page).Context(ctx).Do(call...)
		})
		if absent(err) {
			return nil, nil
		}
		if err != nil {
			return nil, fmt.Errorf("read what the certificate map %s serves: %w", certificateMap, err)
		}
		for _, entry := range entries.CertificateMapEntries {
			if entry.Hostname != "" {
				bound = append(bound, entry.Hostname)
			}
		}
		if page = entries.NextPageToken; page == "" {
			break
		}
	}
	slices.Sort(bound)
	return bound, nil
}

func (p certificates) Discard(ctx context.Context, cert provider.Certificate, progress edge.Progress) error {
	if !cert.Issued() {
		return nil
	}
	_, certificates, err := p.certificates(ctx)
	if err != nil {
		return err
	}
	current, err := attempted(ctx, func(call ...googleapi.CallOption) (*certmanager.Certificate, error) {
		return certificates.Projects.Locations.Certificates.Get(cert.ID).Context(ctx).Do(call...)
	})
	if absent(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read the certificate %s: %w", cert.ID, err)
	}
	say(progress, "discarding the certificate "+cert.ID)
	if err := p.awaitCertificates(ctx, certificates, "discard "+cert.ID,
		func(call ...googleapi.CallOption) (*certmanager.Operation, error) {
			return certificates.Projects.Locations.Certificates.Delete(cert.ID).Context(ctx).Do(call...)
		}); err != nil {
		return err
	}
	if current.Managed == nil {
		return nil
	}
	for _, authorization := range current.Managed.DnsAuthorizations {
		if err := p.awaitCertificates(ctx, certificates, "release "+authorization,
			func(call ...googleapi.CallOption) (*certmanager.Operation, error) {
				return certificates.Projects.Locations.DnsAuthorizations.Delete(authorization).Context(ctx).Do(call...)
			}); err != nil {
			return err
		}
	}
	return nil
}

func (p *Provider) awaitCertificates(
	ctx context.Context,
	certificates *certmanager.Service,
	doing string,
	call func(...googleapi.CallOption) (*certmanager.Operation, error),
) error {
	started, err := attempted(ctx, call)
	if err != nil {
		return fmt.Errorf("ask Certificate Manager to %s: %w", doing, err)
	}
	finished, err := until(ctx, "Certificate Manager to "+doing, func() (*certmanager.Operation, error) {
		if started.Done {
			return started, nil
		}
		return attempted(ctx, func(opt ...googleapi.CallOption) (*certmanager.Operation, error) {
			return certificates.Projects.Locations.Operations.Get(started.Name).Context(ctx).Do(opt...)
		})
	}, func(op *certmanager.Operation) bool { return op != nil && op.Done })
	if err != nil {
		return err
	}
	if finished.Error != nil {
		return refusal.Refuse(refusal.CodeNotReady,
			"Certificate Manager refused to %s: %s", doing, finished.Error.Message)
	}
	return nil
}
