package cloudflare

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"slices"
	"strings"
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
	clientCAName              = "ocel origin pull CA"
	embeddedIssuerPrefix      = "data:application/pkix-cert;base64,"
	globalOriginPullsSetting  = "tls_client_auth"
	settingOn                 = "on"
)

var heldStatuses = []string{"initializing", "pending_deployment", "active"}

type zoneClientCertificate struct {
	ID          string    `json:"id"`
	Certificate string    `json:"certificate"`
	Status      string    `json:"status"`
	UploadedOn  time.Time `json:"uploaded_on"`
}

func (c zoneClientCertificate) isUploadedBefore(other zoneClientCertificate) bool {
	if !c.UploadedOn.Equal(other.UploadedOn) {
		return c.UploadedOn.Before(other.UploadedOn)
	}
	return c.ID < other.ID
}

func readClientCAs(zoneName string, held []zoneClientCertificate) ([]string, error) {
	authorities := make([]string, 0, len(held))
	for _, certificate := range held {
		authority, err := readClientCA(zoneName, certificate)
		if err != nil {
			return nil, err
		}
		authorities = append(authorities, authority)
	}
	slices.Sort(authorities)
	return slices.Compact(authorities), nil
}

func readClientCA(zoneName string, held zoneClientCertificate) (string, error) {
	block, _ := pem.Decode([]byte(held.Certificate))
	if block == nil || block.Type != "CERTIFICATE" {
		return "", refuseUnreadableCA(zoneName, held.ID)
	}
	presented, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return "", refuseUnreadableCA(zoneName, held.ID)
	}
	for _, issuer := range presented.IssuingCertificateURL {
		encoded, embedded := strings.CutPrefix(issuer, embeddedIssuerPrefix)
		if !embedded {
			continue
		}
		der, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			continue
		}
		authority, err := x509.ParseCertificate(der)
		if err != nil || !authority.IsCA || presented.CheckSignatureFrom(authority) != nil {
			continue
		}
		return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), nil
	}
	if bytes.Equal(presented.RawIssuer, presented.RawSubject) && presented.CheckSignature(presented.SignatureAlgorithm, presented.RawTBSCertificate, presented.Signature) == nil {
		return string(pem.EncodeToMemory(block)), nil
	}
	return "", refuseUnreadableCA(zoneName, held.ID)
}

func refuseUnreadableCA(zoneName, id string) error {
	return refusal.Refuse(refusal.CodeInvalid,
		"zone %s presents the client certificate %s to origins, and ocel cannot read the CA it chains to, so no origin can be told to trust it: delete it (SSL/TLS > Origin Server > Authenticated Origin Pulls) so ocel uploads one of its own, or upload a self-signed one, and deploy again",
		zoneName, id)
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
		return readClientCAs(zoneName, held)
	}
	certificate, key, err := mintClientCertificate(zoneName, time.Now())
	if err != nil {
		return nil, err
	}
	uploaded, err := p.client.OriginTLSClientAuth.New(ctx, origin_tls_client_auth.OriginTLSClientAuthNewParams{
		ZoneID:      cf.F(zoneID),
		Certificate: cf.F(certificate),
		PrivateKey:  cf.F(key),
	})
	if err != nil {
		return nil, fmt.Errorf("upload the client certificate zone %s presents to origins: %w", zoneName, err)
	}
	return p.keepFirstUpload(ctx, zoneID, zoneName, uploaded.ID)
}

func (p *cloudflare) keepFirstUpload(ctx context.Context, zoneID, zoneName, uploadedID string) ([]string, error) {
	held, err := p.listClientCertificates(ctx, zoneID)
	if err != nil {
		return nil, err
	}
	ours := slices.IndexFunc(held, func(listed zoneClientCertificate) bool { return listed.ID == uploadedID })
	if ours < 0 {
		return readClientCAs(zoneName, held)
	}
	anotherUploadedFirst := slices.ContainsFunc(held, func(listed zoneClientCertificate) bool { return listed.isUploadedBefore(held[ours]) })
	if !anotherUploadedFirst {
		return readClientCAs(zoneName, held)
	}
	if _, err := p.client.OriginTLSClientAuth.Delete(ctx, uploadedID, origin_tls_client_auth.OriginTLSClientAuthDeleteParams{ZoneID: cf.F(zoneID)}); err != nil {
		return nil, fmt.Errorf("delete the client certificate this run uploaded to zone %s after another run uploaded one first: %w", zoneName, err)
	}
	return readClientCAs(zoneName, slices.Delete(held, ours, ours+1))
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

func (p *cloudflare) listClientCertificates(ctx context.Context, zoneID string) ([]zoneClientCertificate, error) {
	listed := p.client.OriginTLSClientAuth.ListAutoPaging(ctx, origin_tls_client_auth.OriginTLSClientAuthListParams{ZoneID: cf.F(zoneID)})
	var held []zoneClientCertificate
	for listed.Next() {
		var read zoneClientCertificate
		if err := json.Unmarshal([]byte(listed.Current().JSON.RawJSON()), &read); err != nil {
			return nil, fmt.Errorf("read a client certificate of zone %s: %w", zoneID, err)
		}
		if slices.Contains(heldStatuses, read.Status) && read.Certificate != "" {
			held = append(held, read)
		}
	}
	if err := listed.Err(); err != nil {
		return nil, fmt.Errorf("list the client certificates zone %s presents to origins: %w", zoneID, err)
	}
	return held, nil
}

func mintClientCertificate(zoneName string, now time.Time) (certificate, key string, err error) {
	authorityKey, authority, err := mintClientCA(zoneName, now)
	if err != nil {
		return "", "", err
	}
	private, err := rsa.GenerateKey(rand.Reader, clientCertificateBits)
	if err != nil {
		return "", "", fmt.Errorf("generate the client certificate's key: %w", err)
	}
	serial, err := drawSerial()
	if err != nil {
		return "", "", err
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
		IssuingCertificateURL: []string{embeddedIssuerPrefix + base64.StdEncoding.EncodeToString(authority.Raw)},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, authority, &private.PublicKey, authorityKey)
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

func mintClientCA(zoneName string, now time.Time) (*rsa.PrivateKey, *x509.Certificate, error) {
	private, err := rsa.GenerateKey(rand.Reader, clientCertificateBits)
	if err != nil {
		return nil, nil, fmt.Errorf("generate the client certificate CA's key: %w", err)
	}
	serial, err := drawSerial()
	if err != nil {
		return nil, nil, err
	}
	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: clientCAName + " " + zoneName},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(clientCertificateLifetime),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		IsCA:                  true,
		MaxPathLenZero:        true,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &private.PublicKey, private)
	if err != nil {
		return nil, nil, fmt.Errorf("sign the client certificate CA: %w", err)
	}
	authority, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, nil, fmt.Errorf("read the client certificate CA: %w", err)
	}
	return private, authority, nil
}

func drawSerial() (*big.Int, error) {
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, fmt.Errorf("draw a certificate serial: %w", err)
	}
	return serial, nil
}
