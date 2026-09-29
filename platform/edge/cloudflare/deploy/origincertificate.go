package cloudflare

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"

	cf "github.com/cloudflare/cloudflare-go/v4"
	"github.com/cloudflare/cloudflare-go/v4/origin_ca_certificates"
	"github.com/cloudflare/cloudflare-go/v4/shared"
	"github.com/cloudflare/cloudflare-go/v4/ssl"

	"github.com/ocelhq/ocel/pkg/edge"
)

const (
	originCertificateValidity     = ssl.RequestValidity365
	originCertificateOrganization = "ocel"
)

func (p *cloudflare) issueOriginCertificate(ctx context.Context, hostname string) (edge.OriginCertificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return edge.OriginCertificate{}, fmt.Errorf("generate the key %s's origin certificate is issued for: %w", hostname, err)
	}
	request, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject:  pkix.Name{CommonName: hostname, Organization: []string{originCertificateOrganization}},
		DNSNames: []string{hostname},
	}, key)
	if err != nil {
		return edge.OriginCertificate{}, fmt.Errorf("sign the request for %s's origin certificate: %w", hostname, err)
	}
	issued, err := p.client.OriginCACertificates.New(ctx, origin_ca_certificates.OriginCACertificateNewParams{
		Csr:               cf.F(string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: request}))),
		Hostnames:         cf.F([]string{hostname}),
		RequestType:       cf.F(shared.CertificateRequestTypeOriginECC),
		RequestedValidity: cf.F(originCertificateValidity),
	})
	if err != nil {
		return edge.OriginCertificate{}, fmt.Errorf("ask Cloudflare's origin CA for a certificate covering %s: %w", hostname, err)
	}
	block, _ := pem.Decode([]byte(issued.Certificate))
	if block == nil {
		return edge.OriginCertificate{}, fmt.Errorf("the origin CA answered %s with no PEM certificate", hostname)
	}
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return edge.OriginCertificate{}, fmt.Errorf("read the origin certificate issued for %s: %w", hostname, err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return edge.OriginCertificate{}, fmt.Errorf("encode the key of %s's origin certificate: %w", hostname, err)
	}
	if issued.ID == "" {
		return edge.OriginCertificate{}, errors.New("cloudflare's origin CA issued a certificate with no id, so it could never be revoked")
	}
	return edge.OriginCertificate{
		ID:          issued.ID,
		Certificate: issued.Certificate,
		Key:         string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})),
		ExpiresAt:   leaf.NotAfter,
	}, nil
}

func (p *cloudflare) revokeOriginCertificate(ctx context.Context, id string) error {
	if _, err := p.client.OriginCACertificates.Delete(ctx, id); err != nil {
		return fmt.Errorf("revoke origin certificate %s: %w", id, err)
	}
	return nil
}
