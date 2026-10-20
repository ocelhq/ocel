package gcp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ocelhq/ocel/platform/gcp/provider/internal/bootstrap"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/googletest"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/names"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/policy"
)

type policyServer struct {
	reads  atomic.Int64
	writes atomic.Int64

	refusals int64
}

func (p *policyServer) open(t *testing.T) *gcloud.Clients {
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
	return googletest.WithTierBuckets(&gcloud.Clients{Names: names.New("ocel", "acme-prod"), Region: "europe-west1", Endpoint: server.URL})
}

func TestAPolicyChangedUnderTheGrantIsReReadAndWrittenAgain(t *testing.T) {
	t.Parallel()

	server := &policyServer{refusals: 1}
	if err := (composed(bootstrap.Engine{Clients: server.open(t)})).GrantRunAs(context.Background(), "ocel-production"); err != nil {
		t.Fatalf("grantRunAs() against a policy that changed once under it = %v, want the grant to land: another run bootstrapping the sibling tier writes this same policy", err)
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

	server := &policyServer{refusals: policy.EditAttempts + 1}
	err := (composed(bootstrap.Engine{Clients: server.open(t)})).GrantRunAs(context.Background(), "ocel-production")
	if err == nil {
		t.Fatal("grantRunAs() over a policy that never stops changing = nil, want the refusal that names what the grant is for")
	}
	if !strings.Contains(err.Error(), "ocel-production") {
		t.Errorf("grantRunAs() = %v, want it to name the account the deploy would run apps as", err)
	}
	if got := server.writes.Load(); got != policy.EditAttempts {
		t.Errorf("the grant wrote the policy %d times, want %d: a retry that never gives up keeps a bootstrap open", got, policy.EditAttempts)
	}
}
