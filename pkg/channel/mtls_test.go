package channel

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"io"
	"strings"
	"testing"
	"time"
)

func TestNewIdentity(t *testing.T) {
	t.Parallel()

	identity, err := NewIdentity()
	if err != nil {
		t.Fatalf("NewIdentity() error = %v", err)
	}
	leaf := identity.Leaf()

	t.Run("is an ecdsa P-256 key", func(t *testing.T) {
		key, ok := leaf.PublicKey.(*ecdsa.PublicKey)
		if !ok {
			t.Fatalf("leaf public key is %T, want *ecdsa.PublicKey", leaf.PublicKey)
		}
		if key.Curve != elliptic.P256() {
			t.Fatalf("leaf curve = %v, want P-256", key.Curve.Params().Name)
		}
	})

	t.Run("anchors itself", func(t *testing.T) {
		if !leaf.IsCA {
			t.Error("leaf IsCA = false, want the cert to act as its own anchor")
		}
		if !leaf.BasicConstraintsValid {
			t.Error("leaf BasicConstraintsValid = false, want true")
		}
		if leaf.KeyUsage&x509.KeyUsageDigitalSignature == 0 {
			t.Error("leaf key usage is missing DigitalSignature")
		}
		if leaf.KeyUsage&x509.KeyUsageCertSign == 0 {
			t.Error("leaf key usage is missing CertSign")
		}
	})

	t.Run("serves both ends of the channel", func(t *testing.T) {
		var client, server bool
		for _, usage := range leaf.ExtKeyUsage {
			switch usage {
			case x509.ExtKeyUsageClientAuth:
				client = true
			case x509.ExtKeyUsageServerAuth:
				server = true
			}
		}
		if !client || !server {
			t.Errorf("leaf ext key usage = %v, want both ClientAuth and ServerAuth", leaf.ExtKeyUsage)
		}
		if leaf.Subject.CommonName != "localhost" {
			t.Errorf("leaf common name = %q, want %q", leaf.Subject.CommonName, "localhost")
		}
		if len(leaf.DNSNames) != 1 || leaf.DNSNames[0] != "localhost" {
			t.Errorf("leaf DNS names = %v, want [localhost]", leaf.DNSNames)
		}
	})

	t.Run("lives one subprocess, and tolerates a skewed clock", func(t *testing.T) {
		now := time.Now()
		if skew := now.Sub(leaf.NotBefore); skew < 25*time.Second || skew > 90*time.Second {
			t.Errorf("leaf NotBefore is %s before now, want roughly 30s of skew tolerance", skew)
		}
		if life := leaf.NotAfter.Sub(now); life < 5*time.Hour || life > 7*time.Hour {
			t.Errorf("leaf lifetime is %s, want roughly 6h", life)
		}
	})

	t.Run("mints a fresh identity every time", func(t *testing.T) {
		other, err := NewIdentity()
		if err != nil {
			t.Fatalf("NewIdentity() error = %v", err)
		}
		if string(other.CertificateDER()) == string(identity.CertificateDER()) {
			t.Error("two identities share a certificate, want each spawn to be pairwise")
		}
	})
}

func TestCertificatePEM(t *testing.T) {
	t.Parallel()

	identity, err := NewIdentity()
	if err != nil {
		t.Fatalf("NewIdentity() error = %v", err)
	}

	t.Run("round trips through PEM", func(t *testing.T) {
		t.Parallel()
		got, err := ParseCertificatePEM(identity.CertificatePEM())
		if err != nil {
			t.Fatalf("ParseCertificatePEM() error = %v", err)
		}
		if !got.Equal(identity.Leaf()) {
			t.Error("ParseCertificatePEM() returned a different certificate")
		}
	})

	t.Run("refuses anything that is not a certificate", func(t *testing.T) {
		t.Parallel()
		for _, tc := range []struct {
			name string
			pem  string
		}{
			{"nothing at all", ""},
			{"unarmoured bytes", "not a pem block"},
			{"a truncated block", strings.SplitAfter(identity.CertificatePEM(), "\n")[0]},
			{"another pem type", "-----BEGIN PRIVATE KEY-----\nAAAA\n-----END PRIVATE KEY-----\n"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()
				if _, err := ParseCertificatePEM(tc.pem); err == nil {
					t.Fatalf("ParseCertificatePEM(%q) error = nil, want an error", tc.pem)
				}
			})
		}
	})
}

func TestReadinessLineCarriesTheServerCertificate(t *testing.T) {
	t.Parallel()

	identity, err := NewIdentity()
	if err != nil {
		t.Fatalf("NewIdentity() error = %v", err)
	}

	t.Run("round trips the address and the certificate", func(t *testing.T) {
		t.Parallel()
		for _, addr := range []string{
			"unix:/tmp/ocel-provider-abc123/provider.sock",
			"tcp:127.0.0.1:54321",
		} {
			line := FormatReadinessLine(addr, identity.CertificateDER())
			if strings.Contains(line, "\n") {
				t.Fatalf("FormatReadinessLine() = %q, want a single stdout line", line)
			}
			gotAddr, gotCert, ok := ParseReadinessLine(line)
			if !ok {
				t.Fatalf("ParseReadinessLine(%q) ok = false, want true", line)
			}
			if gotAddr != addr {
				t.Fatalf("ParseReadinessLine(%q) addr = %q, want %q", line, gotAddr, addr)
			}
			if !gotCert.Equal(identity.Leaf()) {
				t.Fatalf("ParseReadinessLine(%q) returned a different certificate", line)
			}
		}
	})

	t.Run("refuses a line that carries no certificate", func(t *testing.T) {
		t.Parallel()
		b64 := base64.StdEncoding.EncodeToString(identity.CertificateDER())
		for _, tc := range []struct {
			name string
			line string
		}{
			{"nothing at all", ""},
			{"an unrelated log line", "listening on socket...\n"},
			{"a sentinel with a typo", "OCEL_READY_TYPO unix:/tmp/x.sock " + b64},
			{"the sentinel named midway through a line", "some log line mentioning OCEL_READY midway " + b64},
			{"an address with no certificate", "OCEL_READY unix:/tmp/x.sock"},
			{"a certificate with no address", "OCEL_READY " + b64},
			{"an empty certificate field", "OCEL_READY unix:/tmp/x.sock "},
			{"a certificate that is not base64", "OCEL_READY unix:/tmp/x.sock not-base64!"},
			{"base64 that is not a certificate", "OCEL_READY unix:/tmp/x.sock " + base64.StdEncoding.EncodeToString([]byte("nope"))},
		} {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()
				if _, _, ok := ParseReadinessLine(tc.line); ok {
					t.Fatalf("ParseReadinessLine(%q) ok = true, want false", tc.line)
				}
			})
		}
	})
}

func TestLooksLikeReadinessLine(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		line string
		want bool
	}{
		{name: "a line the older wire format produced", line: "OCEL_READY unix:/tmp/x.sock", want: true},
		{name: "a line this wire format produces", line: "OCEL_READY unix:/tmp/x.sock Zm9v", want: true},
		{name: "an unrelated log line", line: "listening on socket..."},
		{name: "the bare sentinel with nothing after it", line: "OCEL_READY"},
		{name: "the sentinel named midway through a line", line: "some log line mentioning OCEL_READY midway"},
		{name: "a sentinel with a typo", line: "OCEL_READY_TYPO unix:/tmp/x.sock"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := LooksLikeReadinessLine(tc.line); got != tc.want {
				t.Fatalf("LooksLikeReadinessLine(%q) = %v, want %v", tc.line, got, tc.want)
			}
		})
	}
}

func TestPinnedHandshake(t *testing.T) {
	t.Parallel()

	server, err := NewIdentity()
	if err != nil {
		t.Fatalf("NewIdentity() error = %v", err)
	}
	client, err := NewIdentity()
	if err != nil {
		t.Fatalf("NewIdentity() error = %v", err)
	}
	stranger, err := NewIdentity()
	if err != nil {
		t.Fatalf("NewIdentity() error = %v", err)
	}

	t.Run("the paired client is served", func(t *testing.T) {
		t.Parallel()
		addr := listenTLS(t, server.ServerConfig(client.Leaf()))
		if err := shake(addr, client.ClientConfig(server.Leaf())); err != nil {
			t.Fatalf("the paired client was refused: %v", err)
		}
	})

	t.Run("a client presenting no certificate is refused", func(t *testing.T) {
		t.Parallel()
		addr := listenTLS(t, server.ServerConfig(client.Leaf()))
		anonymous := client.ClientConfig(server.Leaf())
		anonymous.Certificates = nil
		if err := shake(addr, anonymous); err == nil {
			t.Fatal("a client with no certificate completed the handshake, want it refused")
		}
	})

	t.Run("a client presenting another certificate is refused", func(t *testing.T) {
		t.Parallel()
		addr := listenTLS(t, server.ServerConfig(client.Leaf()))
		if err := shake(addr, stranger.ClientConfig(server.Leaf())); err == nil {
			t.Fatal("a client with an unpaired certificate completed the handshake, want it refused")
		}
	})

	t.Run("a server presenting another certificate is refused", func(t *testing.T) {
		t.Parallel()
		addr := listenTLS(t, stranger.ServerConfig(client.Leaf()))
		if err := shake(addr, client.ClientConfig(server.Leaf())); err == nil {
			t.Fatal("an impersonating server completed the handshake, want it refused")
		}
	})

	t.Run("trusts nothing beyond the one certificate it was handed", func(t *testing.T) {
		t.Parallel()
		if pool := client.ClientConfig(server.Leaf()).RootCAs; len(pool.Subjects()) != 1 {
			t.Errorf("the client pins %d roots, want exactly the server certificate", len(pool.Subjects()))
		}
		config := server.ServerConfig(client.Leaf())
		if len(config.ClientCAs.Subjects()) != 1 {
			t.Errorf("the server pins %d client anchors, want exactly the client certificate", len(config.ClientCAs.Subjects()))
		}
		if config.ClientAuth != tls.RequireAndVerifyClientCert {
			t.Errorf("the server ClientAuth = %v, want RequireAndVerifyClientCert", config.ClientAuth)
		}
		if config.MinVersion != tls.VersionTLS12 {
			t.Errorf("the server MinVersion = %x, want TLS 1.2", config.MinVersion)
		}
	})
}

func listenTLS(t *testing.T, config *tls.Config) string {
	t.Helper()
	ln, err := tls.Listen("tcp", "127.0.0.1:0", config)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				_, _ = conn.Write([]byte("hello"))
			}()
		}
	}()
	return ln.Addr().String()
}

func shake(addr string, config *tls.Config) error {
	conn, err := tls.Dial("tcp", addr, config)
	if err != nil {
		return err
	}
	defer conn.Close()
	if err := conn.Handshake(); err != nil {
		return err
	}
	if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return err
	}
	buf := make([]byte, 5)
	_, err = io.ReadFull(conn, buf)
	return err
}
