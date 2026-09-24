package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type authority struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
}

func newAuthority(t *testing.T) authority {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "Caddy Local Authority"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	raw, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(raw)
	if err != nil {
		t.Fatal(err)
	}
	return authority{cert: cert, key: key}
}

func (a authority) rootPEM() string {
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: a.cert.Raw}))
}

func (a authority) issue(t *testing.T, hostname string) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: hostname},
		DNSNames:     []string{hostname},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	raw, err := x509.CreateCertificate(rand.Reader, template, a.cert, &key.PublicKey, a.key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{raw}, PrivateKey: key}
}

func answering(t *testing.T, leaf tls.Certificate, handler http.HandlerFunc) string {
	t.Helper()
	server := httptest.NewUnstartedServer(handler)
	server.TLS = &tls.Config{Certificates: []tls.Certificate{leaf}}
	server.StartTLS()
	t.Cleanup(server.Close)
	return server.Listener.Addr().String()
}

func holdingRoot(t *testing.T, root authority) *admin {
	t.Helper()
	held, _ := served(t)
	said, err := json.Marshal(map[string]string{"id": "local", "root_certificate": root.rootPEM()})
	if err != nil {
		t.Fatal(err)
	}
	held.body = string(said)
	return held
}

func probed(t *testing.T, at, hostname string) (int, string, string) {
	t.Helper()
	var out, errs strings.Builder
	return probe(os.Getenv(socketEnv), at, hostname, &out, &errs), out.String(), errs.String()
}

func TestTheProbeReadsTheEdgeTheProxyNamesForAHostnameOverACertificateItsOwnAuthorityIssued(t *testing.T) {
	root := newAuthority(t)
	held := holdingRoot(t, root)
	var asked string
	at := answering(t, root.issue(t, "web.localhost"), func(w http.ResponseWriter, r *http.Request) {
		asked = r.Host
		w.Header().Set(edge.HeaderEdge, "box")
		w.WriteHeader(http.StatusOK)
	})

	code, out, errs := probed(t, at, "web.localhost")
	if code != 0 {
		t.Fatalf("probe = %d: %q", code, errs)
	}
	if strings.TrimSpace(out) != "box" {
		t.Errorf("probe printed %q, want the edge the proxy named for the hostname", out)
	}
	if asked != "web.localhost" {
		t.Errorf("the proxy was asked for %q, want the hostname probed: it routes on the name and not on the address it listens at", asked)
	}
	if calls := held.asked(); len(calls) != 1 || calls[0] != "GET /pki/ca/local" {
		t.Errorf("the probe asked the admin api %v, want only the local authority's root: it is the one issuer a .localhost name can be served under", calls)
	}
}

func TestTheProbeRefusesACertificateTheProxysOwnAuthorityNeverIssued(t *testing.T) {
	holdingRoot(t, newAuthority(t))
	at := answering(t, newAuthority(t).issue(t, "web.localhost"), func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set(edge.HeaderEdge, "box")
	})

	code, out, errs := probed(t, at, "web.localhost")
	if code != exitNotServingYet {
		t.Fatalf("probe over a certificate nothing on the box vouches for = %d, want %d: a header read off a handshake nobody verified proves nothing served the name", code, exitNotServingYet)
	}
	if strings.TrimSpace(out) != "" {
		t.Errorf("probe printed %q over a refused handshake", out)
	}
	if !strings.Contains(errs, "x509") && !strings.Contains(errs, "certificate") {
		t.Errorf("probe said %q, want the refused chain named so a settle that gives up has a cause to report", errs)
	}
}

func TestTheProbeReadsTheEdgeOffTheHostnameAndNotOffWhereARedirectLands(t *testing.T) {
	root := newAuthority(t)
	holdingRoot(t, root)
	at := answering(t, root.issue(t, "web.localhost"), func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/elsewhere" {
			w.Header().Set(edge.HeaderEdge, "box")
			return
		}
		http.Redirect(w, r, "/elsewhere", http.StatusFound)
	})

	code, out, errs := probed(t, at, "web.localhost")
	if code != 0 {
		t.Fatalf("probe = %d: %q", code, errs)
	}
	if strings.TrimSpace(out) != "" {
		t.Errorf("probe printed %q, want nothing: it followed a redirect and read the edge off wherever the chain landed", out)
	}
}

func TestTheProbeSaysWhatStoppedItWhenNothingListens(t *testing.T) {
	holdingRoot(t, newAuthority(t))
	at := listening(t, func(taken net.Conn) { taken.Close() })

	code, _, errs := probed(t, at, "web.localhost")
	if code != exitNotServingYet {
		t.Fatalf("probe over a peer that never spoke tls = %d, want %d: the settle keeps waiting on that", code, exitNotServingYet)
	}
	if strings.TrimSpace(errs) == "" {
		t.Error("probe said nothing about why it reached no edge")
	}
}

func TestAProxyThatHoldsNoLocalAuthorityYetIsNotServingYet(t *testing.T) {
	held, _ := served(t)
	held.status = http.StatusNotFound
	held.body = `{"error":"no certificate authority configured with id: local"}`
	at := listening(t, func(taken net.Conn) { taken.Close() })

	code, out, errs := probed(t, at, "web.localhost")
	if code != exitNotServingYet {
		t.Fatalf("probe over a proxy whose local authority does not exist yet = %d (%q), want %d: the proxy creates it the first time it issues under it, which a bind of a .localhost name is about to make happen", code, errs, exitNotServingYet)
	}
	if strings.TrimSpace(out) != "" {
		t.Errorf("probe printed %q with no authority to verify anything against", out)
	}
	if !strings.Contains(errs, "authority") {
		t.Errorf("probe said %q, want the missing authority named as the cause", errs)
	}
}

func TestAnAdminApiThatFailsIsStillAnError(t *testing.T) {
	held, _ := served(t)
	held.status = http.StatusInternalServerError
	held.body = `{"error":"loading pki app: boom"}`
	at := listening(t, func(taken net.Conn) { taken.Close() })

	if code, _, errs := probed(t, at, "web.localhost"); code != exitRefused {
		t.Errorf("probe over an admin api that failed = %d (%q), want %d: a broken proxy is not a hostname still converging", code, errs, exitRefused)
	}
}
