package gcp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestAFailedProjectNumberReadIsAskedAgainByTheNextCaller(t *testing.T) {
	t.Parallel()

	var reads atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if reads.Add(1) == 1 {
			w.WriteHeader(http.StatusForbidden)
			w.Write([]byte(`{"error":{"code":403,"message":"permission still propagating"}}`))
			return
		}
		w.Write([]byte(`{"projectId":"acme-prod","projectNumber":"123456789"}`))
	}))
	t.Cleanup(server.Close)
	c := &clients{Names: Names{namespace: "ocel", project: "acme-prod"}, region: "europe-west1", endpoint: server.URL}
	const domain = "@gcp-sa-pubsub.iam.gserviceaccount.com"

	if _, err := c.ReadServiceAgent(context.Background(), domain); err == nil {
		t.Fatal("ReadServiceAgent succeeded on a refused project read")
	}
	got, err := c.ReadServiceAgent(context.Background(), domain)
	if err != nil {
		t.Fatalf("ReadServiceAgent after the refusal cleared: %v", err)
	}
	if want := "serviceAccount:service-123456789" + domain; got != want {
		t.Errorf("ReadServiceAgent = %q, want %q", got, want)
	}
	if _, err := c.ReadServiceAgent(context.Background(), domain); err != nil {
		t.Fatalf("ReadServiceAgent once the number is known: %v", err)
	}
	if n := reads.Load(); n != 2 {
		t.Errorf("the project was read %d times, want 2: once refused, once remembered", n)
	}
}

func TestLoggingOpensOneSharedClientAgainstTheEmulator(t *testing.T) {
	t.Parallel()

	c := &clients{Names: Names{namespace: "ocel", project: "acme-prod"}, region: "europe-west1", endpoint: "http://127.0.0.1:1"}

	first, err := c.Logging()
	if err != nil {
		t.Fatalf("Logging: %v", err)
	}
	t.Cleanup(func() { first.Close() })
	second, err := c.Logging()
	if err != nil {
		t.Fatalf("Logging again: %v", err)
	}
	if first != second {
		t.Error("Logging opened a second client, want the first remembered")
	}
}
