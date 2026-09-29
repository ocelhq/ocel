package cloudflare

import (
	"cmp"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"slices"
	"time"

	cf "github.com/cloudflare/cloudflare-go/v4"
	"github.com/cloudflare/cloudflare-go/v4/origin_tls_client_auth"
)

const (
	clientCertificateBits     = 2048
	clientCertificateLifetime = 10 * 365 * 24 * time.Hour
	clientCertificateName     = "ocel origin pull"
)

var presentedStatuses = []string{"initializing", "pending_deployment", "active"}

type zoneClientCertificate struct {
	Certificate string    `json:"certificate"`
	Status      string    `json:"status"`
	UploadedOn  time.Time `json:"uploaded_on"`
}

func (p *cloudflare) ensureClientCertificate(ctx context.Context, hostname string) (string, error) {
	accountID := p.accountID()
	if accountID == "" {
		return "", fmt.Errorf("%s is not set; it is required to read the client certificate Cloudflare presents to origins", envAccountID)
	}
	zoneID, zoneName, err := p.resolveZone(ctx, accountID, routeBaseDomain(hostname))
	if err != nil {
		return "", err
	}
	presented, err := p.readPresentedClientCertificate(ctx, zoneID)
	if err != nil {
		return "", err
	}
	if presented == "" {
		if err := p.uploadClientCertificate(ctx, zoneID, zoneName); err != nil {
			return "", err
		}
		if presented, err = p.readPresentedClientCertificate(ctx, zoneID); err != nil {
			return "", err
		}
		if presented == "" {
			return "", fmt.Errorf("zone %s lists no client certificate it presents to origins after one was uploaded to it", zoneName)
		}
	}
	return presented, p.ensureOriginPulls(ctx, zoneID, zoneName)
}

func (p *cloudflare) readPresentedClientCertificate(ctx context.Context, zoneID string) (string, error) {
	listed := p.client.OriginTLSClientAuth.ListAutoPaging(ctx, origin_tls_client_auth.OriginTLSClientAuthListParams{ZoneID: cf.F(zoneID)})
	var presented []zoneClientCertificate
	for listed.Next() {
		var read zoneClientCertificate
		if err := json.Unmarshal([]byte(listed.Current().JSON.RawJSON()), &read); err != nil {
			return "", fmt.Errorf("read a client certificate of zone %s: %w", zoneID, err)
		}
		if slices.Contains(presentedStatuses, read.Status) && read.Certificate != "" {
			presented = append(presented, read)
		}
	}
	if err := listed.Err(); err != nil {
		return "", fmt.Errorf("list the client certificates zone %s presents to origins: %w", zoneID, err)
	}
	if len(presented) == 0 {
		return "", nil
	}
	return slices.MaxFunc(presented, func(a, b zoneClientCertificate) int {
		return cmp.Or(a.UploadedOn.Compare(b.UploadedOn), cmp.Compare(a.Certificate, b.Certificate))
	}).Certificate, nil
}

func (p *cloudflare) uploadClientCertificate(ctx context.Context, zoneID, zoneName string) error {
	certificate, key, err := mintClientCertificate(zoneName, time.Now())
	if err != nil {
		return err
	}
	if _, err := p.client.OriginTLSClientAuth.New(ctx, origin_tls_client_auth.OriginTLSClientAuthNewParams{
		ZoneID:      cf.F(zoneID),
		Certificate: cf.F(certificate),
		PrivateKey:  cf.F(key),
	}); err != nil {
		return fmt.Errorf("upload the client certificate zone %s presents to origins: %w", zoneName, err)
	}
	return nil
}

func (p *cloudflare) ensureOriginPulls(ctx context.Context, zoneID, zoneName string) error {
	current, err := p.client.OriginTLSClientAuth.Settings.Get(ctx, origin_tls_client_auth.SettingGetParams{ZoneID: cf.F(zoneID)})
	if err != nil {
		return fmt.Errorf("read whether zone %s presents a client certificate to origins: %w", zoneName, err)
	}
	if current.Enabled {
		return nil
	}
	if _, err := p.client.OriginTLSClientAuth.Settings.Update(ctx, origin_tls_client_auth.SettingUpdateParams{
		ZoneID:  cf.F(zoneID),
		Enabled: cf.F(true),
	}); err != nil {
		return fmt.Errorf("turn on zone-level authenticated origin pulls for %s: %w", zoneName, err)
	}
	return nil
}

func mintClientCertificate(zoneName string, now time.Time) (certificate, key string, err error) {
	private, err := rsa.GenerateKey(rand.Reader, clientCertificateBits)
	if err != nil {
		return "", "", fmt.Errorf("generate the client certificate's key: %w", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return "", "", fmt.Errorf("draw the client certificate's serial: %w", err)
	}
	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: clientCertificateName + " " + zoneName},
		DNSNames:              []string{zoneName},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(clientCertificateLifetime),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &private.PublicKey, private)
	if err != nil {
		return "", "", fmt.Errorf("sign the client certificate: %w", err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(private)
	if err != nil {
		return "", "", fmt.Errorf("encode the client certificate's key: %w", err)
	}
	certificate = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	key = string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}))
	return certificate, key, nil
}
