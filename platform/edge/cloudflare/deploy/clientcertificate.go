package cloudflare

import (
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
	"github.com/cloudflare/cloudflare-go/v4/zones"

	"github.com/ocelhq/ocel/pkg/refusal"
)

const (
	clientCertificateBits     = 2048
	clientCertificateLifetime = 10 * 365 * 24 * time.Hour
	clientCertificateName     = "ocel origin pull"
	globalOriginPullsSetting  = "tls_client_auth"
	settingOn                 = "on"
)

var heldStatuses = []string{"initializing", "pending_deployment", "active"}

type zoneClientCertificate struct {
	Certificate string `json:"certificate"`
	Status      string `json:"status"`
}

func (p *cloudflare) ensureClientCertificates(ctx context.Context, hostname string) ([]string, error) {
	zoneID, zoneName, err := p.resolveHostnameZone(ctx, hostname, "read the client certificates Cloudflare presents to origins")
	if err != nil {
		return nil, err
	}
	pulls, err := p.readOriginPulls(ctx, zoneID, zoneName)
	if err != nil {
		return nil, err
	}
	held, err := p.listClientCertificates(ctx, zoneID)
	if err != nil {
		return nil, err
	}
	if err := pulls.refuseShared(zoneName, len(held) > 0); err != nil {
		return nil, err
	}
	if len(held) > 0 {
		return held, nil
	}
	certificate, key, err := mintClientCertificate(zoneName, time.Now())
	if err != nil {
		return nil, err
	}
	if _, err := p.client.OriginTLSClientAuth.New(ctx, origin_tls_client_auth.OriginTLSClientAuthNewParams{
		ZoneID:      cf.F(zoneID),
		Certificate: cf.F(certificate),
		PrivateKey:  cf.F(key),
	}); err != nil {
		return nil, fmt.Errorf("upload the client certificate zone %s presents to origins: %w", zoneName, err)
	}
	return p.listClientCertificates(ctx, zoneID)
}

func (p *cloudflare) presentClientCertificates(ctx context.Context, hostname string) error {
	zoneID, zoneName, err := p.resolveHostnameZone(ctx, hostname, "turn on the client certificate Cloudflare presents to origins")
	if err != nil {
		return err
	}
	pulls, err := p.readOriginPulls(ctx, zoneID, zoneName)
	if err != nil {
		return err
	}
	if pulls.ZoneLevel {
		return nil
	}
	if err := pulls.refuseShared(zoneName, true); err != nil {
		return err
	}
	if _, err := p.client.OriginTLSClientAuth.Settings.Update(ctx, origin_tls_client_auth.SettingUpdateParams{
		ZoneID:  cf.F(zoneID),
		Enabled: cf.F(true),
	}); err != nil {
		return fmt.Errorf("turn on zone-level authenticated origin pulls for %s: %w", zoneName, err)
	}
	return nil
}

type originPulls struct {
	ZoneLevel bool
	Global    bool
}

func (p *cloudflare) readOriginPulls(ctx context.Context, zoneID, zoneName string) (originPulls, error) {
	zoneLevel, err := p.client.OriginTLSClientAuth.Settings.Get(ctx, origin_tls_client_auth.SettingGetParams{ZoneID: cf.F(zoneID)})
	if err != nil {
		return originPulls{}, fmt.Errorf("read whether zone %s presents a client certificate to origins: %w", zoneName, err)
	}
	global, err := p.client.Zones.Settings.Get(ctx, globalOriginPullsSetting, zones.SettingGetParams{ZoneID: cf.F(zoneID)})
	if err != nil {
		return originPulls{}, fmt.Errorf("read whether zone %s presents Cloudflare's shared client certificate to origins: %w", zoneName, err)
	}
	var read struct {
		Value string `json:"value"`
	}
	if err := json.Unmarshal([]byte(global.JSON.RawJSON()), &read); err != nil {
		return originPulls{}, fmt.Errorf("read whether zone %s presents Cloudflare's shared client certificate to origins: %w", zoneName, err)
	}
	return originPulls{ZoneLevel: zoneLevel.Enabled, Global: read.Value == settingOn}, nil
}

func (o originPulls) refuseShared(zoneName string, holdsOwn bool) error {
	const fix = "make every origin you run in it trust a client certificate of your own uploaded to the zone (SSL/TLS > Origin Server > Authenticated Origin Pulls), turn zone-level authenticated origin pulls on, and deploy again"
	switch {
	case o.ZoneLevel && !holdsOwn:
		return refusal.Refuse(refusal.CodeInvalid,
			"zone %s presents Cloudflare's shared client certificate to origins: zone-level authenticated origin pulls are on and the zone holds no certificate of its own, and an origin in it may trust only that one: "+fix,
			zoneName)
	case !o.ZoneLevel && o.Global:
		return refusal.Refuse(refusal.CodeInvalid,
			"zone %s presents Cloudflare's shared client certificate to origins: global authenticated origin pulls are on and zone-level ones are off, and turning zone-level ones on would switch every origin in it to the zone's own certificate at once: "+fix,
			zoneName)
	}
	return nil
}

func (p *cloudflare) resolveHostnameZone(ctx context.Context, hostname, doing string) (id, name string, err error) {
	accountID, err := requireAccountID(doing)
	if err != nil {
		return "", "", err
	}
	return p.resolveZone(ctx, accountID, routeBaseDomain(hostname))
}

func (p *cloudflare) listClientCertificates(ctx context.Context, zoneID string) ([]string, error) {
	listed := p.client.OriginTLSClientAuth.ListAutoPaging(ctx, origin_tls_client_auth.OriginTLSClientAuthListParams{ZoneID: cf.F(zoneID)})
	var held []string
	for listed.Next() {
		var read zoneClientCertificate
		if err := json.Unmarshal([]byte(listed.Current().JSON.RawJSON()), &read); err != nil {
			return nil, fmt.Errorf("read a client certificate of zone %s: %w", zoneID, err)
		}
		if slices.Contains(heldStatuses, read.Status) && read.Certificate != "" {
			held = append(held, read.Certificate)
		}
	}
	if err := listed.Err(); err != nil {
		return nil, fmt.Errorf("list the client certificates zone %s presents to origins: %w", zoneID, err)
	}
	slices.Sort(held)
	return slices.Compact(held), nil
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
