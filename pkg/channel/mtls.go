package channel

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"time"
)

const ClientCertEnvVar = "OCEL_CLIENT_CERT"

const (
	clockSkewAllowance = 30 * time.Second
	certificateLife    = 6 * time.Hour
)

type Identity struct {
	cert tls.Certificate
}

func NewIdentity() (*Identity, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("channel: generate channel key: %w", err)
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, fmt.Errorf("channel: draw a certificate serial: %w", err)
	}

	now := time.Now()
	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "localhost"},
		DNSNames:              []string{"localhost"},
		NotBefore:             now.Add(-clockSkewAllowance),
		NotAfter:              now.Add(certificateLife),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
	}

	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return nil, fmt.Errorf("channel: self-sign the channel certificate: %w", err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, fmt.Errorf("channel: parse the channel certificate: %w", err)
	}

	return &Identity{cert: tls.Certificate{
		Certificate: [][]byte{der},
		PrivateKey:  key,
		Leaf:        leaf,
	}}, nil
}

func (i *Identity) Leaf() *x509.Certificate { return i.cert.Leaf }

func (i *Identity) CertificateDER() []byte { return i.cert.Certificate[0] }

func (i *Identity) CertificatePEM() string {
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: i.CertificateDER()}))
}

func (i *Identity) ServerConfig(client *x509.Certificate) *tls.Config {
	anchor := x509.NewCertPool()
	anchor.AddCert(client)
	return &tls.Config{
		Certificates: []tls.Certificate{i.cert},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    anchor,
		MinVersion:   tls.VersionTLS12,
		NextProtos:   []string{"http/1.1"},
	}
}

func (i *Identity) ClientConfig(server *x509.Certificate) *tls.Config {
	anchor := x509.NewCertPool()
	anchor.AddCert(server)
	return &tls.Config{
		Certificates: []tls.Certificate{i.cert},
		RootCAs:      anchor,
		ServerName:   "localhost",
		MinVersion:   tls.VersionTLS12,
		NextProtos:   []string{"http/1.1"},
	}
}

func ParseCertificatePEM(encoded string) (*x509.Certificate, error) {
	block, _ := pem.Decode([]byte(encoded))
	if block == nil {
		return nil, errors.New("channel: no PEM block found")
	}
	if block.Type != "CERTIFICATE" {
		return nil, fmt.Errorf("channel: PEM block is a %q, want a CERTIFICATE", block.Type)
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("channel: parse certificate: %w", err)
	}
	return cert, nil
}
