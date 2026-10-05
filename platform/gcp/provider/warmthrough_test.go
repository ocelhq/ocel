package gcp

import (
	"context"
	"crypto/x509"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestWarmingThroughALoadBalancerDialsItsAddressAndAsksForTheHostname(t *testing.T) {
	asked := make(chan string, 4)
	balancer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked <- r.Method + " " + r.Host + r.URL.Path
	}))
	t.Cleanup(balancer.Close)
	roots := x509.NewCertPool()
	roots.AddCert(balancer.Certificate())

	if err := (&Provider{warmRoots: roots}).warmThrough(context.Background(), "https://example.com/healthz", balancer.Listener.Addr().String()); err != nil {
		t.Fatalf("warmThrough() = %v", err)
	}

	select {
	case got := <-asked:
		if got != "HEAD example.com/healthz" {
			t.Errorf("the load balancer was asked %q, want HEAD example.com/healthz: its url map routes by hostname", got)
		}
	default:
		t.Error("warming asked the load balancer nothing")
	}
}
