package gcp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

type policyServer struct {
	reads  atomic.Int64
	writes atomic.Int64

	refusals int64
}

func (p *policyServer) open(t *testing.T) *clients {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, ":getIamPolicy"):
			p.reads.Add(1)
			w.Write([]byte(`{"etag":"BwXhoLA="}`))
		case strings.HasSuffix(r.URL.Path, ":setIamPolicy"):
			if p.writes.Add(1) <= p.refusals {
				w.WriteHeader(http.StatusConflict)
				w.Write([]byte(`{"error":{"code":409,"message":"There were concurrent policy changes"}}`))
				return
			}
			w.Write([]byte(`{"etag":"BwXhoLB="}`))
		default:
			t.Errorf("the grant called %s %s, which nothing here serves", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	return &clients{Names: Names{namespace: "ocel", project: "acme-prod"}, region: "europe-west1", endpoint: server.URL}
}

func TestAPolicyChangedUnderTheGrantIsReReadAndWrittenAgain(t *testing.T) {
	t.Parallel()

	server := &policyServer{refusals: 1}
	if err := (bootstrapper{clients: server.open(t)}).grantRunAs(context.Background(), "ocel-production"); err != nil {
		t.Fatalf("grantRunAs() against a policy that changed once under it = %v, want the grant to land: another run bootstrapping the sibling class writes this same policy", err)
	}
	if got := server.writes.Load(); got != 2 {
		t.Errorf("the grant wrote the policy %d times, want 2: a 409 says the read the write was built on is stale", got)
	}
	if got := server.reads.Load(); got != 2 {
		t.Errorf("the grant read the policy %d times, want 2: writing the same stale policy again earns the same 409", got)
	}
}

func TestAPolicyThatKeepsChangingUnderTheGrantIsRefusedRatherThanRetriedForever(t *testing.T) {
	t.Parallel()

	server := &policyServer{refusals: grantAttempts + 1}
	err := (bootstrapper{clients: server.open(t)}).grantRunAs(context.Background(), "ocel-production")
	if err == nil {
		t.Fatal("grantRunAs() over a policy that never settles = nil, want the refusal that names what the grant is for")
	}
	if !strings.Contains(err.Error(), "ocel-production") {
		t.Errorf("grantRunAs() = %v, want it to name the account the deploy would run apps as", err)
	}
	if got := server.writes.Load(); got != grantAttempts {
		t.Errorf("the grant wrote the policy %d times, want %d: a retry that never gives up holds a bootstrap open", got, grantAttempts)
	}
}
