package cloudflare

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"slices"
	"strings"
	"time"

	cf "github.com/cloudflare/cloudflare-go/v4"
	"github.com/cloudflare/cloudflare-go/v4/dns"
	"github.com/cloudflare/cloudflare-go/v4/origin_tls_client_auth"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/refusal"
)

const (
	clientCertificateBits     = 2048
	clientCertificateLifetime = 10 * 365 * 24 * time.Hour
	clientCertificateName     = "ocel origin pull"
	clientCAName              = "ocel origin pull CA"
	embeddedIssuerPrefix      = "data:application/pkix-cert;base64,"
	maxZoneClientCertificates = 10
)

var (
	heldStatuses     = []string{"initializing", "pending_deployment", "active"}
	releasedStatuses = []string{"pending_deletion", "deleted", "deletion_timed_out"}
)

type certificateOwner struct {
	origin OriginScope
	tier   environment.Tier
}

func (o certificateOwner) String() string { return string(o.tier) + " of origin " + o.origin.String() }

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

func (c zoneClientCertificate) readOwner() (certificateOwner, bool) {
	return readCertificateOwner(c.Certificate)
}

func readCertificateOwner(certificatePEM string) (certificateOwner, bool) {
	block, _ := pem.Decode([]byte(certificatePEM))
	if block == nil || block.Type != "CERTIFICATE" {
		return certificateOwner{}, false
	}
	presented, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return certificateOwner{}, false
	}
	readable, named := strings.CutPrefix(presented.Subject.CommonName, clientCertificateName+" ")
	if !named || len(presented.Subject.OrganizationalUnit) != 1 {
		return certificateOwner{}, false
	}
	origin, named := parseOriginScope(readable)
	return certificateOwner{origin: origin, tier: environment.Tier(presented.Subject.OrganizationalUnit[0])}, named
}

type zoneClientCertificates struct {
	zoneName string
	owner    certificateOwner
	held     []zoneClientCertificate
}

func (z zoneClientCertificates) own() []zoneClientCertificate {
	return slices.DeleteFunc(slices.Clone(z.held), func(c zoneClientCertificate) bool {
		owner, named := c.readOwner()
		return !named || owner != z.owner
	})
}

func (z zoneClientCertificates) current() (zoneClientCertificate, bool) {
	own := z.own()
	if len(own) == 0 {
		return zoneClientCertificate{}, false
	}
	return slices.MinFunc(own, func(a, b zoneClientCertificate) int {
		if a.isUploadedBefore(b) {
			return -1
		}
		return 1
	}), true
}

func (z zoneClientCertificates) isOriginsCertificate(association origin_tls_client_auth.AuthenticatedOriginPull) bool {
	if association.CERTID == "" {
		return false
	}
	if owner, named := readCertificateOwner(association.Certificate); named {
		return owner.origin == z.owner.origin
	}
	return slices.ContainsFunc(z.held, func(c zoneClientCertificate) bool {
		owner, named := c.readOwner()
		return c.ID == association.CERTID && named && owner.origin == z.owner.origin
	})
}

func (z zoneClientCertificates) refuseFull() error {
	if _, held := z.current(); held || len(z.held) < maxZoneClientCertificates {
		return nil
	}
	return refusal.Refuse(refusal.CodeInvalid,
		"zone %s holds %d per-hostname client certificates, as many as Cloudflare allows in one zone, and none is the one the %s presents: delete one no hostname uses (SSL/TLS > Origin Server > Authenticated Origin Pulls), or forward the hostname through another zone, and deploy again",
		z.zoneName, len(z.held), z.owner)
}

func (z zoneClientCertificates) refuseForeignAssociation(hostname string, association origin_tls_client_auth.AuthenticatedOriginPull) error {
	if association.CERTID == "" || z.isOriginsCertificate(association) || slices.Contains(releasedStatuses, string(association.CERTStatus)) {
		return nil
	}
	if owner, named := readCertificateOwner(association.Certificate); named {
		return refuseOtherOrigin(fmt.Sprintf("Cloudflare presents origin %s's client certificate for %s, and that origin trusts no other", owner.origin, hostname), hostname, owner.origin)
	}
	return refusal.Refuse(refusal.CodeInvalid,
		"Cloudflare presents the client certificate %s for %s, which ocel did not mint, and the origin behind it may trust no other: remove that hostname's association (SSL/TLS > Origin Server > Authenticated Origin Pulls) and deploy again",
		association.CERTID, hostname)
}

func readClientCA(zoneName string, held zoneClientCertificate) (string, error) {
	authority, ok := readEmbeddedCA(held.Certificate)
	if !ok {
		return "", fmt.Errorf("read the CA the client certificate %s of zone %s chains to: it embeds none ocel can read", held.ID, zoneName)
	}
	return authority, nil
}

func readEmbeddedCA(certificatePEM string) (string, bool) {
	block, _ := pem.Decode([]byte(certificatePEM))
	if block == nil || block.Type != "CERTIFICATE" {
		return "", false
	}
	presented, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return "", false
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
		return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), true
	}
	return "", false
}

func (p *cloudflare) refuseUnreadyClientCertificates(ctx context.Context, tier environment.Tier, hostname string) error {
	accountID, err := p.tierAccount(ctx, tier, "read whether Cloudflare can forward "+hostname+" to its origin with a client certificate")
	if err != nil {
		return err
	}
	zone, err := p.resolveHostnameZone(ctx, accountID, hostname)
	if err != nil {
		return err
	}
	if err := p.requireStrictOrigin(ctx, zone); err != nil {
		return err
	}
	names, err := p.names(ctx)
	if err != nil {
		return err
	}
	certificates, err := p.readZoneClientCertificates(ctx, certificateOwner{origin: names.origin, tier: tier}, zone)
	if err != nil {
		return err
	}
	if err := certificates.refuseFull(); err != nil {
		return err
	}
	association, err := p.readAssociation(ctx, zone, hostname)
	if err != nil {
		return err
	}
	return certificates.refuseForeignAssociation(hostname, association)
}

func (p *cloudflare) ensureClientCertificates(ctx context.Context, tier environment.Tier, hostname string) ([]string, error) {
	accountID, err := p.tierAccount(ctx, tier, "read the client certificate Cloudflare presents to this origin")
	if err != nil {
		return nil, err
	}
	zone, err := p.resolveHostnameZone(ctx, accountID, hostname)
	if err != nil {
		return nil, err
	}
	names, err := p.names(ctx)
	if err != nil {
		return nil, err
	}
	current, _, err := p.ensureOwnClientCertificate(ctx, certificateOwner{origin: names.origin, tier: tier}, zone)
	if err != nil {
		return nil, err
	}
	authority, err := readClientCA(zone.zoneName, current)
	if err != nil {
		return nil, err
	}
	return []string{authority}, nil
}

func (p *cloudflare) ensureOwnClientCertificate(ctx context.Context, owner certificateOwner, zone zoneAccess) (zoneClientCertificate, zoneClientCertificates, error) {
	certificates, err := p.readZoneClientCertificates(ctx, owner, zone)
	if err != nil {
		return zoneClientCertificate{}, zoneClientCertificates{}, err
	}
	if current, held := certificates.current(); held {
		return current, certificates, nil
	}
	if err := certificates.refuseFull(); err != nil {
		return zoneClientCertificate{}, zoneClientCertificates{}, err
	}
	if err := p.recordClientCertificateZone(ctx, owner.tier, zone); err != nil {
		return zoneClientCertificate{}, zoneClientCertificates{}, err
	}
	certificate, key, err := mintClientCertificate(owner.origin.String(), []string{string(owner.tier)}, zone.zoneName, time.Now(), clientCertificateLifetime)
	if err != nil {
		return zoneClientCertificate{}, zoneClientCertificates{}, err
	}
	uploaded, err := p.client.OriginTLSClientAuth.Hostnames.Certificates.New(ctx, origin_tls_client_auth.HostnameCertificateNewParams{
		ZoneID:      cf.F(zone.zoneID),
		Certificate: cf.F(certificate),
		PrivateKey:  cf.F(key),
	})
	if err != nil {
		return zoneClientCertificate{}, zoneClientCertificates{}, refuseZoneCertificatesFailure(zone.zoneName, fmt.Sprintf("upload the client certificate the %s presents in zone %s", certificates.owner, zone.zoneName), err)
	}
	return p.keepFirstUpload(ctx, owner, zone, zoneClientCertificate{ID: uploaded.ID, Certificate: certificate, Status: string(uploaded.Status), UploadedOn: uploaded.UploadedOn})
}

func (p *cloudflare) recordClientCertificateZone(ctx context.Context, tier environment.Tier, zone zoneAccess) error {
	doing := fmt.Sprintf("record that tier %s presents a client certificate in zone %s", tier, zone.zoneName)
	held := recordedZone{ID: zone.zoneID, Name: zone.zoneName}
	err := p.changeTierRecord(ctx, tier, doing, func(recorded *tierRecord) (bool, error) {
		if slices.Contains(recorded.ClientCertificateZones, held) {
			return false, nil
		}
		recorded.ClientCertificateZones = append(recorded.ClientCertificateZones, held)
		return true, nil
	})
	if err != nil {
		return fmt.Errorf("%s: %w", doing, err)
	}
	return nil
}

func (p *cloudflare) keepFirstUpload(ctx context.Context, owner certificateOwner, zone zoneAccess, uploaded zoneClientCertificate) (zoneClientCertificate, zoneClientCertificates, error) {
	certificates, err := p.readZoneClientCertificates(ctx, owner, zone)
	if err != nil {
		return zoneClientCertificate{}, zoneClientCertificates{}, err
	}
	if !slices.ContainsFunc(certificates.held, func(listed zoneClientCertificate) bool { return listed.ID == uploaded.ID }) {
		certificates.held = append(certificates.held, uploaded)
	}
	current, _ := certificates.current()
	if current.ID == uploaded.ID {
		return current, certificates, nil
	}
	if _, err := p.client.OriginTLSClientAuth.Hostnames.Certificates.Delete(ctx, uploaded.ID, origin_tls_client_auth.HostnameCertificateDeleteParams{ZoneID: cf.F(zone.zoneID)}); err != nil {
		return zoneClientCertificate{}, zoneClientCertificates{}, refuseZoneCertificatesFailure(zone.zoneName, fmt.Sprintf("delete the client certificate this run uploaded to zone %s after another run of the %s uploaded one first", zone.zoneName, certificates.owner), err)
	}
	certificates.held = slices.DeleteFunc(certificates.held, func(listed zoneClientCertificate) bool { return listed.ID == uploaded.ID })
	return current, certificates, nil
}

func (p *cloudflare) presentClientCertificates(ctx context.Context, tier environment.Tier, hostname string) error {
	accountID, err := p.tierAccount(ctx, tier, "present this origin's client certificate for "+hostname)
	if err != nil {
		return err
	}
	zone, err := p.resolveHostnameZone(ctx, accountID, hostname)
	if err != nil {
		return err
	}
	names, err := p.names(ctx)
	if err != nil {
		return err
	}
	current, certificates, err := p.ensureOwnClientCertificate(ctx, certificateOwner{origin: names.origin, tier: tier}, zone)
	if err != nil {
		return err
	}
	association, err := p.readAssociation(ctx, zone, hostname)
	if err != nil {
		return err
	}
	if association.CERTID == current.ID && association.Enabled {
		return nil
	}
	if err := certificates.refuseForeignAssociation(hostname, association); err != nil {
		return err
	}
	return p.writeAssociation(ctx, zone, origin_tls_client_auth.HostnameUpdateParamsConfig{
		Hostname: cf.F(hostname),
		CERTID:   cf.F(current.ID),
		Enabled:  cf.F(true),
	}, fmt.Sprintf("present the client certificate of the %s for %s", certificates.owner, hostname))
}

func (p *cloudflare) releaseClientCertificate(ctx context.Context, zone zoneAccess, origin OriginScope, hostname string) error {
	forwarded, err := p.isForwardedByOrigin(ctx, zone.zoneID, origin, hostname)
	if err != nil || forwarded {
		return err
	}
	held, err := p.listZoneClientCertificates(ctx, zone)
	if err != nil {
		return err
	}
	certificates := zoneClientCertificates{zoneName: zone.zoneName, owner: certificateOwner{origin: origin}, held: held}
	association, err := p.readAssociation(ctx, zone, hostname)
	if err != nil || !certificates.isOriginsCertificate(association) {
		return err
	}
	return p.writeAssociation(ctx, zone, origin_tls_client_auth.HostnameUpdateParamsConfig{
		Hostname: cf.F(hostname),
		CERTID:   cf.F(association.CERTID),
		Enabled:  cf.Null[bool](),
	}, fmt.Sprintf("stop presenting origin %s's client certificate for %s", certificates.owner.origin, hostname))
}

func (p *cloudflare) isForwardedByOrigin(ctx context.Context, zoneID string, origin OriginScope, hostname string) (bool, error) {
	live, err := p.listAddressRecords(ctx, zoneID, hostname)
	if err != nil {
		return false, err
	}
	return slices.ContainsFunc(live, func(rec dns.RecordResponse) bool {
		held, written := parseRecordOwner(rec.Comment)
		return written && held.isForwarding() && !held.isOtherOrigin(origin)
	}), nil
}

type zonedClientCertificate struct {
	zone zoneAccess
	held zoneClientCertificate
}

func (p *cloudflare) listTierClientCertificates(ctx context.Context, accountID string, owner certificateOwner) ([]zonedClientCertificate, error) {
	recorded, err := p.readTierRecord(ctx, owner.tier)
	if err != nil {
		return nil, err
	}
	var found []zonedClientCertificate
	for _, held := range recorded.ClientCertificateZones {
		zone := zoneAccess{accountID: accountID, zoneID: held.ID, zoneName: held.Name}
		certificates, err := p.readZoneClientCertificates(ctx, owner, zone)
		if err != nil {
			return nil, err
		}
		for _, own := range certificates.own() {
			found = append(found, zonedClientCertificate{zone: zone, held: own})
		}
	}
	return found, nil
}

func (p *cloudflare) planRemoveClientCertificates(ctx context.Context, accountID string, owner certificateOwner) ([]edge.PlanChange, error) {
	found, err := p.listTierClientCertificates(ctx, accountID, owner)
	if err != nil {
		return nil, err
	}
	changes := make([]edge.PlanChange, 0, len(found))
	for _, zoned := range found {
		changes = append(changes, edge.PlanChange{
			Kind: kindOriginPullCertificate, Name: zoned.zone.zoneName + "/" + zoned.held.ID, Action: edge.PlanDelete,
			Reason: "the client certificate this origin presents for the tier's hostnames in the zone",
		})
	}
	return changes, nil
}

func (p *cloudflare) deleteClientCertificates(ctx context.Context, accountID string, owner certificateOwner) error {
	found, err := p.listTierClientCertificates(ctx, accountID, owner)
	if err != nil {
		return err
	}
	var errs []error
	for _, zoned := range found {
		if _, err := p.client.OriginTLSClientAuth.Hostnames.Certificates.Delete(ctx, zoned.held.ID, origin_tls_client_auth.HostnameCertificateDeleteParams{ZoneID: cf.F(zoned.zone.zoneID)}); err != nil {
			errs = append(errs, refuseZoneCertificatesFailure(zoned.zone.zoneName, fmt.Sprintf("delete the client certificate %s this origin presents for tier %s in zone %s", zoned.held.ID, owner.tier, zoned.zone.zoneName), err))
		}
	}
	return errors.Join(errs...)
}

func (p *cloudflare) readAssociation(ctx context.Context, zone zoneAccess, hostname string) (origin_tls_client_auth.AuthenticatedOriginPull, error) {
	association, err := p.client.OriginTLSClientAuth.Hostnames.Get(ctx, hostname, origin_tls_client_auth.HostnameGetParams{ZoneID: cf.F(zone.zoneID)})
	switch {
	case hasStatus(err, http.StatusNotFound):
		return origin_tls_client_auth.AuthenticatedOriginPull{}, nil
	case err != nil:
		return origin_tls_client_auth.AuthenticatedOriginPull{}, refuseZoneCertificatesFailure(zone.zoneName, fmt.Sprintf("read which client certificate zone %s presents for %s", zone.zoneName, hostname), err)
	}
	return *association, nil
}

func (p *cloudflare) writeAssociation(ctx context.Context, zone zoneAccess, config origin_tls_client_auth.HostnameUpdateParamsConfig, doing string) error {
	if _, err := p.client.OriginTLSClientAuth.Hostnames.Update(ctx, origin_tls_client_auth.HostnameUpdateParams{
		ZoneID: cf.F(zone.zoneID),
		Config: cf.F([]origin_tls_client_auth.HostnameUpdateParamsConfig{config}),
	}); err != nil {
		return refuseZoneCertificatesFailure(zone.zoneName, doing, err)
	}
	return nil
}

func (p *cloudflare) readZoneClientCertificates(ctx context.Context, owner certificateOwner, zone zoneAccess) (zoneClientCertificates, error) {
	if err := refuseUnknownTier("client certificates", owner.tier); err != nil {
		return zoneClientCertificates{}, err
	}
	held, err := p.listZoneClientCertificates(ctx, zone)
	if err != nil {
		return zoneClientCertificates{}, err
	}
	return zoneClientCertificates{zoneName: zone.zoneName, owner: owner, held: held}, nil
}

func (p *cloudflare) listZoneClientCertificates(ctx context.Context, zone zoneAccess) ([]zoneClientCertificate, error) {
	listed := p.client.OriginTLSClientAuth.Hostnames.Certificates.ListAutoPaging(ctx, origin_tls_client_auth.HostnameCertificateListParams{ZoneID: cf.F(zone.zoneID)})
	var held []zoneClientCertificate
	for listed.Next() {
		var read zoneClientCertificate
		if err := json.Unmarshal([]byte(listed.Current().JSON.RawJSON()), &read); err != nil {
			return nil, fmt.Errorf("read a client certificate of zone %s: %w", zone.zoneName, err)
		}
		if slices.Contains(heldStatuses, read.Status) && read.Certificate != "" {
			held = append(held, read)
		}
	}
	if err := listed.Err(); err != nil {
		return nil, refuseZoneCertificatesFailure(zone.zoneName, fmt.Sprintf("list the per-hostname client certificates of zone %s", zone.zoneName), err)
	}
	return held, nil
}

func mintClientCertificate(subject string, units []string, dnsName string, now time.Time, lifetime time.Duration) (certificate, key string, err error) {
	authorityKey, authority, err := mintClientCA(subject, units, now, lifetime)
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
		Subject:               pkix.Name{CommonName: clientCertificateName + " " + subject, OrganizationalUnit: units},
		DNSNames:              []string{dnsName},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(lifetime),
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

func mintClientCA(subject string, units []string, now time.Time, lifetime time.Duration) (*rsa.PrivateKey, *x509.Certificate, error) {
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
		Subject:               pkix.Name{CommonName: clientCAName + " " + subject, OrganizationalUnit: units},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(lifetime),
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
