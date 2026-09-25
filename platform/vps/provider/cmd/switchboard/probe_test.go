package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
	"github.com/ocelhq/ocel/platform/vps/provider/switchboard"
)

func issued(t *testing.T, hostname string, from, until time.Time) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: hostname},
		DNSNames:     []string{hostname},
		NotBefore:    from,
		NotAfter:     until,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	raw, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{raw}, PrivateKey: key}
}

func current(t *testing.T, hostname string) tls.Certificate {
	t.Helper()
	return issued(t, hostname, time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
}

func answering(t *testing.T, leaf tls.Certificate, handler http.HandlerFunc) string {
	t.Helper()
	server := httptest.NewUnstartedServer(handler)
	server.TLS = &tls.Config{Certificates: []tls.Certificate{leaf}}
	server.StartTLS()
	t.Cleanup(server.Close)
	return server.Listener.Addr().String()
}

func listening(t *testing.T, answer func(net.Conn)) string {
	t.Helper()
	held, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { held.Close() })
	go func() {
		for {
			taken, err := held.Accept()
			if err != nil {
				return
			}
			go answer(taken)
		}
	}()
	return held.Addr().String()
}

func declining(t *testing.T) string {
	t.Helper()
	return listening(t, func(taken net.Conn) {
		spoken := tls.Server(taken, &tls.Config{
			GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
				return nil, errors.New("no certificate available for this name yet")
			},
		})
		_ = spoken.Handshake()
		spoken.Close()
	})
}

func silent(t *testing.T) string {
	t.Helper()
	return listening(t, func(taken net.Conn) { taken.Close() })
}

func switchboardAnswers(w http.ResponseWriter, r *http.Request) {
	w.Header().Set(edge.HeaderEdge, "box")
	w.Header().Set(switchboard.HeardHeader, "https "+r.Host+" "+r.Host)
}

func TestTheProbeReadsTheEdgeTheBoxNamesForAHostnameItServesACertificateFor(t *testing.T) {
	var asked, path string
	at := answering(t, current(t, "web.localhost"), func(w http.ResponseWriter, r *http.Request) {
		asked, path = r.Host, r.URL.Path
		switchboardAnswers(w, r)
	})

	code, out, errs := ran(t, "probe", "--at", at, "web.localhost")
	if code != 0 {
		t.Fatalf("probe = %d: %q", code, errs)
	}
	if strings.TrimSpace(out) != "box" {
		t.Errorf("probe printed %q, want the edge the box named for the hostname", out)
	}
	if asked != "web.localhost" || path != edge.LivenessProbePath {
		t.Errorf("the box was asked for %s%s, want %s%s: it routes on the name and not on the address it listens at", asked, path, "web.localhost", edge.LivenessProbePath)
	}
}

func TestTheProbeRefusesABoxWhoseAppsWouldHearAnotherSchemeOrHost(t *testing.T) {
	for heard, wanted := range map[string]string{
		"http web.localhost web.localhost":          "X-Forwarded-Proto",
		"https 127.0.0.1:8480 web.localhost":        "keep the Host header",
		"https shop.example.test shop.example.test": "keep the Host header",
		"https web.localhost shop.example.test":     "X-Forwarded-Host",
		"https web.localhost":                       "X-Forwarded-Host",
		"https":                                     "keep the Host header",
	} {
		at := answering(t, current(t, "web.localhost"), func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set(edge.HeaderEdge, "box")
			w.Header().Set(switchboard.HeardHeader, heard)
		})
		code, out, errs := ran(t, "probe", "--at", at, "web.localhost")
		if code != exitNotServingYet {
			t.Errorf("probe of a box whose apps hear %q = %d %q, want %d: the node runtime builds its urls from both", heard, code, out, exitNotServingYet)
		}
		if !strings.Contains(errs, wanted) {
			t.Errorf("probe of a box whose apps hear %q said %q, want %s named as what to fix", heard, errs, wanted)
		}
	}
}

func TestTheProbePassesABoxWhoseAppsHearHttpsForTheHostnameAsked(t *testing.T) {
	at := answering(t, current(t, "web.localhost"), func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set(edge.HeaderEdge, "box")
		w.Header().Set(switchboard.HeardHeader, "https web.localhost WEB.localhost")
	})
	if code, out, errs := ran(t, "probe", "--at", at, "web.localhost"); code != 0 || strings.TrimSpace(out) != "box" {
		t.Errorf("probe = %d %q %q, want box", code, out, errs)
	}
}

func TestTheProbeRefusesAnAnswerThatSaysNothingOfWhatItsAppsWouldHear(t *testing.T) {
	at := answering(t, current(t, "web.localhost"), func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set(edge.HeaderEdge, "box")
	})
	code, out, errs := ran(t, "probe", "--at", at, "web.localhost")
	if code != exitNotServingYet {
		t.Errorf("probe of an answer naming the box but not what its apps hear = %d %q, want %d: only the switchboard says what its apps would hear, so anything else answered", code, out, exitNotServingYet)
	}
	if !strings.Contains(errs, switchboard.Name) {
		t.Errorf("probe said %q, want it to name %s as what never answered", errs, switchboard.Name)
	}
}

func TestTheProbeTrustsNoAuthorityOfAnyOneFrontProxy(t *testing.T) {
	at := answering(t, current(t, "web.localhost"), switchboardAnswers)

	if code, out, errs := ran(t, "probe", "--at", at, "web.localhost"); code != 0 || strings.TrimSpace(out) != "box" {
		t.Errorf("probe over a certificate no public root vouches for = %d, %q, %q, want the edge read: a .localhost name is served under whatever authority the front proxy keeps, and the probe must not know which proxy that is", code, out, errs)
	}
}

func TestTheProbeRefusesACertificateThatDoesNotServeTheName(t *testing.T) {
	now := time.Now()
	for what, leaf := range map[string]tls.Certificate{
		"for another name":    current(t, "fallback.localhost"),
		"that has expired":    issued(t, "web.localhost", now.Add(-2*time.Hour), now.Add(-time.Hour)),
		"not yet in its term": issued(t, "web.localhost", now.Add(time.Hour), now.Add(2*time.Hour)),
	} {
		at := answering(t, leaf, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set(edge.HeaderEdge, "box")
		})
		code, out, errs := ran(t, "probe", "--at", at, "web.localhost")
		if code != exitNotServingYet {
			t.Errorf("probe over a certificate %s = %d, want %d: a header read over a certificate that does not serve the name proves nothing served it", what, code, exitNotServingYet)
		}
		if strings.TrimSpace(out) != "" {
			t.Errorf("probe over a certificate %s printed %q", what, out)
		}
		if strings.Count(strings.TrimSpace(errs), "\n") != 0 || !strings.Contains(errs, "web.localhost") {
			t.Errorf("probe over a certificate %s said %q, want one line naming the hostname for the settle to report as its cause", what, errs)
		}
	}
}

func TestTheProbeReadsTheEdgeOffTheHostnameAndNotOffWhereARedirectLands(t *testing.T) {
	at := answering(t, current(t, "web.localhost"), func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/elsewhere" {
			switchboardAnswers(w, r)
			return
		}
		http.Redirect(w, r, "/elsewhere", http.StatusFound)
	})

	code, out, errs := ran(t, "probe", "--at", at, "web.localhost")
	if code != exitNotServingYet || strings.TrimSpace(out) != "" {
		t.Errorf("probe = %d %q %q, want %d and nothing printed: it followed a redirect and read the edge off wherever the chain landed", code, out, errs, exitNotServingYet)
	}
}

func TestTheProbeSaysWhatStoppedItWhenTheBoxServesNothingYet(t *testing.T) {
	for what, at := range map[string]string{
		"a peer that never spoke tls":           silent(t),
		"a front proxy with no certificate yet": declining(t),
		"nothing listening at all":              freeAddress(t),
	} {
		code, out, errs := ran(t, "probe", "--at", at, "web.localhost")
		if code != exitNotServingYet {
			t.Errorf("probe over %s = %d, want %d: the settle keeps waiting on that", what, code, exitNotServingYet)
		}
		if strings.TrimSpace(out) != "" || strings.TrimSpace(errs) == "" || strings.Count(strings.TrimSpace(errs), "\n") != 0 {
			t.Errorf("probe over %s printed %q and said %q, want nothing printed and one line saying why", what, out, errs)
		}
	}
}

func TestTheLeafIsTakenOffTheHandshakeWithTheBox(t *testing.T) {
	served := current(t, "shop.example.com")
	at := answering(t, served, func(http.ResponseWriter, *http.Request) {})

	code, out, errs := ran(t, "leaf", "--at", at, "shop.example.com")
	if code != 0 {
		t.Fatalf("leaf off a box that served a certificate = %d: %q", code, errs)
	}
	block, _ := pem.Decode([]byte(out))
	if block == nil || block.Type != "CERTIFICATE" || string(block.Bytes) != string(served.Certificate[0]) {
		t.Errorf("leaf wrote %q, want the certificate the box presented on the handshake", out)
	}
}

func TestAHandshakeThatFailedForAnyReasonButAMissingCertificateIsNotReportedAsPending(t *testing.T) {
	for what, at := range map[string]struct {
		address string
		want    int
	}{
		"a front proxy that has not obtained one for this name": {declining(t), exitNotServingYet},
		"a peer that never spoke tls at all":                    {silent(t), exitUnservable},
		"nothing listening at all":                              {freeAddress(t), exitRefused},
	} {
		if code, _, errs := ran(t, "leaf", "--at", at.address, "shop.example.com"); code != at.want {
			t.Errorf("leaf over %s = %d, want %d: %q", what, code, at.want, errs)
		}
	}
}

func TestTheLoopbackVerbsTakeOneHostname(t *testing.T) {
	for _, argv := range [][]string{{"leaf"}, {"probe"}, {"leaf", "a.example.com", "b.example.com"}, {"probe", "--at"}} {
		if code, _, _ := ran(t, argv...); code != exitRefused {
			t.Errorf("%v = %d, want usage", argv, code)
		}
	}
}
