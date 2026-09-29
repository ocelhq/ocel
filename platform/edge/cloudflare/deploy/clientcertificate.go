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
	"errors"
	"fmt"
	"math/big"
	"slices"
	"strings"
	"time"

	cf "github.com/cloudflare/cloudflare-go/v4"
	"github.com/cloudflare/cloudflare-go/v4/origin_tls_client_auth"
)

const (
	clientCertificateBits     = 2048
	clientCertificateLifetime = 365 * 24 * time.Hour
	clientCertificateRenewal  = 30 * 24 * time.Hour
	clientCertificateName     = "ocel origin pull"
	activeStatus              = "active"
)

var presentedStatuses = []string{"initializing", "pending_deployment", activeStatus}

type zoneClientCertificate struct {
	ID          string    `json:"id"`
	Certificate string    `json:"certificate"`
	Status      string    `json:"status"`
	UploadedOn  time.Time `json:"uploaded_on"`
}

type stagedClientCertificate struct {
	certificate string
	key         string
}

func (p *cloudflare) stageClientCertificates(ctx context.Context, hostname string) ([]string, error) {
	zoneID, zoneName, err := p.clientCertificateZone(ctx, hostname)
	if err != nil {
		return nil, err
	}
	presented, err := p.readPresentedClientCertificates(ctx, zoneID)
	if err != nil {
		return nil, err
	}
	p.clientMu.Lock()
	staged, pending := p.staged[zoneID]
	p.clientMu.Unlock()
	if !pending && dueForSuccessor(presented, time.Now()) {
		certificate, key, err := mintClientCertificate(zoneName, time.Now())
		if err != nil {
			return nil, err
		}
		staged = stagedClientCertificate{certificate: certificate, key: key}
		p.clientMu.Lock()
		if p.staged == nil {
			p.staged = map[string]stagedClientCertificate{}
		}
		p.staged[zoneID] = staged
		p.clientMu.Unlock()
		pending = true
	}
	if !pending {
		if presented, err = p.retireSupersededClientCertificates(ctx, zoneID, presented); err != nil {
			return nil, err
		}
	}
	trusted := make([]string, 0, len(presented)+1)
	for _, listed := range presented {
		trusted = append(trusted, listed.Certificate)
	}
	if pending {
		trusted = append(trusted, staged.certificate)
	}
	slices.Sort(trusted)
	return slices.Compact(trusted), nil
}

func (p *cloudflare) presentClientCertificate(ctx context.Context, hostname string) error {
	zoneID, zoneName, err := p.clientCertificateZone(ctx, hostname)
	if err != nil {
		return err
	}
	p.clientMu.Lock()
	staged, pending := p.staged[zoneID]
	p.clientMu.Unlock()
	if pending {
		if _, err := p.client.OriginTLSClientAuth.New(ctx, origin_tls_client_auth.OriginTLSClientAuthNewParams{
			ZoneID:      cf.F(zoneID),
			Certificate: cf.F(staged.certificate),
			PrivateKey:  cf.F(staged.key),
		}); err != nil {
			return fmt.Errorf("upload the client certificate zone %s presents to origins: %w", zoneName, err)
		}
		p.clientMu.Lock()
		delete(p.staged, zoneID)
		p.clientMu.Unlock()
	}
	return p.ensureOriginPulls(ctx, zoneID, zoneName)
}

func (p *cloudflare) clientCertificateZone(ctx context.Context, hostname string) (id, name string, err error) {
	accountID := p.accountID()
	if accountID == "" {
		return "", "", fmt.Errorf("%s is not set; it is required to read the client certificate Cloudflare presents to origins", envAccountID)
	}
	return p.resolveZone(ctx, accountID, routeBaseDomain(hostname))
}

func latestClientCertificate(presented []zoneClientCertificate) zoneClientCertificate {
	return slices.MaxFunc(presented, func(a, b zoneClientCertificate) int {
		return cmp.Or(a.UploadedOn.Compare(b.UploadedOn), cmp.Compare(a.Certificate, b.Certificate))
	})
}

func dueForSuccessor(presented []zoneClientCertificate, now time.Time) bool {
	if len(presented) == 0 {
		return true
	}
	leaf, minted := mintedLeaf(latestClientCertificate(presented).Certificate)
	return minted && now.Add(clientCertificateRenewal).After(leaf.NotAfter)
}

func mintedLeaf(certificate string) (*x509.Certificate, bool) {
	block, _ := pem.Decode([]byte(certificate))
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, false
	}
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, false
	}
	return leaf, strings.HasPrefix(leaf.Subject.CommonName, clientCertificateName+" ")
}

func (p *cloudflare) retireSupersededClientCertificates(ctx context.Context, zoneID string, presented []zoneClientCertificate) ([]zoneClientCertificate, error) {
	if len(presented) < 2 {
		return presented, nil
	}
	latest := latestClientCertificate(presented)
	if latest.Status != activeStatus {
		return presented, nil
	}
	kept := make([]zoneClientCertificate, 0, len(presented))
	var errs []error
	for _, listed := range presented {
		if _, minted := mintedLeaf(listed.Certificate); !minted || listed.ID == latest.ID {
			kept = append(kept, listed)
			continue
		}
		if _, err := p.client.OriginTLSClientAuth.Delete(ctx, listed.ID, origin_tls_client_auth.OriginTLSClientAuthDeleteParams{ZoneID: cf.F(zoneID)}); err != nil {
			errs = append(errs, fmt.Errorf("delete the client certificate %s zone %s no longer presents: %w", listed.ID, zoneID, err))
			kept = append(kept, listed)
		}
	}
	return kept, errors.Join(errs...)
}

func (p *cloudflare) readPresentedClientCertificates(ctx context.Context, zoneID string) ([]zoneClientCertificate, error) {
	listed := p.client.OriginTLSClientAuth.ListAutoPaging(ctx, origin_tls_client_auth.OriginTLSClientAuthListParams{ZoneID: cf.F(zoneID)})
	var presented []zoneClientCertificate
	for listed.Next() {
		var read zoneClientCertificate
		if err := json.Unmarshal([]byte(listed.Current().JSON.RawJSON()), &read); err != nil {
			return nil, fmt.Errorf("read a client certificate of zone %s: %w", zoneID, err)
		}
		if slices.Contains(presentedStatuses, read.Status) && read.Certificate != "" {
			presented = append(presented, read)
		}
	}
	if err := listed.Err(); err != nil {
		return nil, fmt.Errorf("list the client certificates zone %s presents to origins: %w", zoneID, err)
	}
	return presented, nil
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
