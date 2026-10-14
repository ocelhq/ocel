package gcp

import (
	"context"
	"crypto/x509"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/edge/gate"
)

func TestAProbeCountsNoOtherAnswerAsEnforced(t *testing.T) {
	for name, answer := range map[string]struct {
		status   int
		cache    string
		robots   string
		enforced bool
	}{
		"the app answering 200":               {http.StatusOK, "", "", false},
		"the app's own 401":                   {http.StatusUnauthorized, "", "", false},
		"a 401 that is cacheable":             {http.StatusUnauthorized, "public, max-age=60", gate.RobotsTag, false},
		"the load balancer's 404 for no host": {http.StatusNotFound, "", "", false},
		"a 503 from a plugin that crashed":    {http.StatusServiceUnavailable, "", "", false},
	} {
		t.Run(name, func(t *testing.T) {
			balancer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Host != "pr-7.example.com" || r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
					t.Errorf("the probe asked %q with credentials %q %q, want the hostname alone and nothing that could pass a gate", r.Host, r.Header.Get("Authorization"), r.Header.Get("Cookie"))
				}
				if answer.cache != "" {
					w.Header().Set("Cache-Control", answer.cache)
				}
				if answer.robots != "" {
					w.Header().Set("X-Robots-Tag", answer.robots)
				}
				w.WriteHeader(answer.status)
			}))
			t.Cleanup(balancer.Close)
			roots := x509.NewCertPool()
			roots.AddCert(balancer.Certificate())

			enforced, err := (&Provider{warmRoots: roots}).probeGate(context.Background(), "pr-7.example.com", balancer.Listener.Addr().String())

			if err != nil {
				t.Fatalf("probeGate = %v", err)
			}
			if enforced != answer.enforced {
				t.Errorf("probeGate = %v, want %v", enforced, answer.enforced)
			}
		})
	}
}

func TestAProbeCountsOnlyTheGatesLoginFormForItsOwnRequestAsEnforced(t *testing.T) {
	for name, answer := range map[string]struct {
		respond  func(w http.ResponseWriter, r *http.Request)
		enforced bool
	}{
		"the gate's login form": {func(w http.ResponseWriter, r *http.Request) {
			verdict := gate.Gate{Key: "k"}.Check(gate.Request{Method: r.Method, Host: r.Host, Target: r.URL.RequestURI(), Header: gate.Header(r.Header)}, time.Now())
			maps.Copy(w.Header(), http.Header(verdict.Response.Header))
			w.WriteHeader(verdict.Response.Status)
			io.WriteString(w, verdict.Response.Body)
		}, true},
		"the app's own 401 marked private and the load balancer's noindex": {func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Cache-Control", gate.CacheControl)
			w.Header().Set("X-Robots-Tag", gate.RobotsTag)
			w.WriteHeader(http.StatusUnauthorized)
			io.WriteString(w, "sign in to the app")
		}, false},
		"a login form for another request": {func(w http.ResponseWriter, _ *http.Request) {
			verdict := gate.Gate{Key: "k"}.Check(gate.Request{Method: http.MethodGet, Target: "/"}, time.Now())
			maps.Copy(w.Header(), http.Header(verdict.Response.Header))
			w.WriteHeader(verdict.Response.Status)
			io.WriteString(w, verdict.Response.Body)
		}, false},
	} {
		t.Run(name, func(t *testing.T) {
			balancer := httptest.NewTLSServer(http.HandlerFunc(answer.respond))
			t.Cleanup(balancer.Close)
			roots := x509.NewCertPool()
			roots.AddCert(balancer.Certificate())

			enforced, err := (&Provider{warmRoots: roots}).probeGate(context.Background(), "pr-7.example.com", balancer.Listener.Addr().String())

			if err != nil {
				t.Fatalf("probeGate = %v", err)
			}
			if enforced != answer.enforced {
				t.Errorf("probeGate = %v, want %v", enforced, answer.enforced)
			}
		})
	}
}

func TestAProbeDoesNotFollowARedirectOntoAnotherHost(t *testing.T) {
	balancer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://elsewhere.example.com/", http.StatusFound)
	}))
	t.Cleanup(balancer.Close)
	roots := x509.NewCertPool()
	roots.AddCert(balancer.Certificate())

	enforced, err := (&Provider{warmRoots: roots}).probeGate(context.Background(), "pr-7.example.com", balancer.Listener.Addr().String())

	if err != nil || enforced {
		t.Errorf("probeGate = %v, %v, want a plain not-enforced answer for a redirect", enforced, err)
	}
}
