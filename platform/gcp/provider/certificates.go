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
	"github.com/ocelhq/ocel/pkg/providerkit"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const (
	certificateActive       = "ACTIVE"
	certificateProvisioning = "PROVISIONING"
	certificateFailed       = "FAILED"
)

const certificateRenewal = "Certificate Manager renews it while the authorization record stands"

const certificateExpiry = 30 * 24 * time.Hour

var issuance = patience{attempts: 100, ceiling: 15 * time.Second}

func (p *Provider) certificatesGlobal() string {
	return "projects/" + p.options.Project + "/locations/global"
}

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

func (p *Provider) Certificate(ctx context.Context, req providerkit.CertificateRequest) (providerkit.Certificate, error) {
	certificates, err := p.clients.Certificates()
	if err != nil {
		return providerkit.Certificate{}, err
	}
	name := p.certificatesGlobal() + "/certificates/" + certificateName(req.Hostname)
	held := providerkit.Certificate{ID: name, Requested: true, Written: req.Held.Written, Owed: req.Held.Owed}

	authorization, err := p.authorized(ctx, certificates, req)
	if err != nil {
		return providerkit.Certificate{}, err
	}
	held, err = req.Prove(ctx, held, []edge.Record{{
		Name:  strings.TrimSuffix(authorization.DnsResourceRecord.Name, "."),
		Type:  edge.RecordTypeCNAME,
		Value: authorization.DnsResourceRecord.Data,
	}})
	if err != nil {
		return held, err
	}
	if err := p.certified(ctx, certificates, req, name, authorization.Name); err != nil {
		return held, err
	}
	return held, nil
}

func (p *Provider) authorized(
	ctx context.Context,
	certificates *certmanager.Service,
	req providerkit.CertificateRequest,
) (*certmanager.DnsAuthorization, error) {
	id := authorizationID(req.Hostname)
	name := p.certificatesGlobal() + "/dnsAuthorizations/" + id
	held, err := attempted(ctx, func(call ...googleapi.CallOption) (*certmanager.DnsAuthorization, error) {
		return certificates.Projects.Locations.DnsAuthorizations.Get(name).Context(ctx).Do(call...)
	})
	switch {
	case err == nil:
		return held, nil
	case !absent(err):
		return nil, fmt.Errorf("read the dns authorization for %s: %w", req.Hostname, err)
	}
	if req.Report != nil {
		req.Report.Say("Asking Certificate Manager to authorize " + authorizedDomain(req.Hostname))
	}
	err = p.awaitCertificates(ctx, certificates, "authorize "+req.Hostname,
		func(call ...googleapi.CallOption) (*certmanager.Operation, error) {
			return certificates.Projects.Locations.DnsAuthorizations.
				Create(p.certificatesGlobal(), &certmanager.DnsAuthorization{Domain: authorizedDomain(req.Hostname)}).
				DnsAuthorizationId(id).Context(ctx).Do(call...)
		})
	if err != nil {
		return nil, err
	}
	held, err = attempted(ctx, func(call ...googleapi.CallOption) (*certmanager.DnsAuthorization, error) {
		return certificates.Projects.Locations.DnsAuthorizations.Get(name).Context(ctx).Do(call...)
	})
	if err != nil {
		return nil, fmt.Errorf("read the dns authorization for %s: %w", req.Hostname, err)
	}
	if held.DnsResourceRecord == nil {
		return nil, providerkit.Refuse(providerkit.CodeNotReady,
			"the dns authorization for %s carries no record to write, and ownership of a domain is proved by writing the one Certificate Manager names",
			req.Hostname)
	}
	return held, nil
}

func (p *Provider) certified(
	ctx context.Context,
	certificates *certmanager.Service,
	req providerkit.CertificateRequest,
	name, authorization string,
) error {
	_, err := attempted(ctx, func(call ...googleapi.CallOption) (*certmanager.Certificate, error) {
		return certificates.Projects.Locations.Certificates.Get(name).Context(ctx).Do(call...)
	})
	switch {
	case err == nil:
	case !absent(err):
		return fmt.Errorf("read the certificate for %s: %w", req.Hostname, err)
	default:
		if req.Report != nil {
			req.Report.Say("Asking Certificate Manager for a certificate covering " + req.Hostname)
		}
		err = p.awaitCertificates(ctx, certificates, "certify "+req.Hostname,
			func(call ...googleapi.CallOption) (*certmanager.Operation, error) {
				return certificates.Projects.Locations.Certificates.
					Create(p.certificatesGlobal(), &certmanager.Certificate{
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
	req providerkit.CertificateRequest,
	name string,
) error {
	settled, err := waiting(ctx, issuance, "Certificate Manager to issue a certificate for "+req.Hostname,
		func() (*certmanager.Certificate, error) {
			return attempted(ctx, func(call ...googleapi.CallOption) (*certmanager.Certificate, error) {
				return certificates.Projects.Locations.Certificates.Get(name).Context(ctx).Do(call...)
			})
		},
		func(held *certmanager.Certificate) bool {
			return held != nil && held.Managed != nil && held.Managed.State != certificateProvisioning
		})
	if err != nil {
		return err
	}
	if settled.Managed.State == certificateActive {
		return nil
	}
	return providerkit.Refuse(providerkit.CodeNotReady,
		"Certificate Manager gave up on the certificate for %s: %s.\n"+
			"The authorization record has to resolve from the public internet before Google will issue: check it, then bind the hostname again",
		req.Hostname, provisioningIssue(settled))
}

func provisioningIssue(held *certmanager.Certificate) string {
	if held.Managed.ProvisioningIssue == nil {
		return held.Managed.State
	}
	if details := held.Managed.ProvisioningIssue.Details; details != "" {
		return details
	}
	return held.Managed.ProvisioningIssue.Reason
}

func (p *Provider) InspectCertificate(
	ctx context.Context,
	_ edge.Kind,
	hostname string,
	cert providerkit.Certificate,
) (providerkit.CertificateHealth, error) {
	if !cert.Held() {
		return providerkit.CertificateHealth{}, nil
	}
	certificates, err := p.clients.Certificates()
	if err != nil {
		return providerkit.CertificateHealth{}, err
	}
	held, err := attempted(ctx, func(call ...googleapi.CallOption) (*certmanager.Certificate, error) {
		return certificates.Projects.Locations.Certificates.Get(cert.ID).Context(ctx).Do(call...)
	})
	if absent(err) {
		return providerkit.CertificateHealth{Terminates: true, Status: "gone", Renewal: certificateRenewal}, nil
	}
	if err != nil {
		return providerkit.CertificateHealth{}, fmt.Errorf("read the certificate %s: %w", cert.ID, err)
	}
	health := providerkit.CertificateHealth{
		Terminates: true,
		Renewal:    certificateRenewal,
		Domains:    covered(held),
	}
	health.Status = certificateProvisioning
	if held.Managed != nil {
		health.Status = held.Managed.State
	}
	health.Issued = health.Status == certificateActive
	health.Covers = slices.Contains(health.Domains, hostname)
	if lapses, err := time.Parse(time.RFC3339, held.ExpireTime); err == nil {
		health.ExpiresAt = lapses.Unix()
		health.ExpiringSoon = time.Until(lapses) < certificateExpiry
	}
	return health, nil
}

func covered(held *certmanager.Certificate) []string {
	if len(held.SanDnsnames) > 0 {
		return slices.Clone(held.SanDnsnames)
	}
	if held.Managed != nil {
		return slices.Clone(held.Managed.Domains)
	}
	return nil
}

func (p *Provider) Entered(ctx context.Context, certificateMap string) ([]string, error) {
	certificates, err := p.clients.Certificates()
	if err != nil {
		return nil, err
	}
	parent := p.certificatesGlobal() + "/certificateMaps/" + certificateMap
	var bound []string
	for page := ""; ; {
		held, err := attempted(ctx, func(call ...googleapi.CallOption) (*certmanager.ListCertificateMapEntriesResponse, error) {
			return certificates.Projects.Locations.CertificateMaps.CertificateMapEntries.
				List(parent).PageToken(page).Context(ctx).Do(call...)
		})
		if absent(err) {
			return nil, nil
		}
		if err != nil {
			return nil, fmt.Errorf("read what the certificate map %s serves: %w", certificateMap, err)
		}
		for _, entry := range held.CertificateMapEntries {
			if entry.Hostname != "" {
				bound = append(bound, entry.Hostname)
			}
		}
		if page = held.NextPageToken; page == "" {
			break
		}
	}
	slices.Sort(bound)
	return bound, nil
}

func (p *Provider) DiscardCertificate(ctx context.Context, cert providerkit.Certificate, report providerkit.Reporter) error {
	if !cert.Held() {
		return nil
	}
	certificates, err := p.clients.Certificates()
	if err != nil {
		return err
	}
	held, err := attempted(ctx, func(call ...googleapi.CallOption) (*certmanager.Certificate, error) {
		return certificates.Projects.Locations.Certificates.Get(cert.ID).Context(ctx).Do(call...)
	})
	if absent(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read the certificate %s: %w", cert.ID, err)
	}
	say(report, "discarding the certificate "+cert.ID)
	if err := p.awaitCertificates(ctx, certificates, "discard "+cert.ID,
		func(call ...googleapi.CallOption) (*certmanager.Operation, error) {
			return certificates.Projects.Locations.Certificates.Delete(cert.ID).Context(ctx).Do(call...)
		}); err != nil {
		return err
	}
	if held.Managed == nil {
		return nil
	}
	for _, authorization := range held.Managed.DnsAuthorizations {
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
	settled, err := until(ctx, "Certificate Manager to "+doing, func() (*certmanager.Operation, error) {
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
	if settled.Error != nil {
		return providerkit.Refuse(providerkit.CodeNotReady,
			"Certificate Manager refused to %s: %s", doing, settled.Error.Message)
	}
	return nil
}

var _ providerkit.Certifier = (*Provider)(nil)
