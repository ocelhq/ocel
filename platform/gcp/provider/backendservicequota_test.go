package gcp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"google.golang.org/api/compute/v1"

	"github.com/ocelhq/ocel/platform/gcp/provider/edges/alb"
)

func quoting(t *testing.T, quotas []*compute.Quota) *Provider {
	t.Helper()
	served := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || !strings.HasSuffix(r.URL.Path, "/projects/acme-prod") {
			t.Errorf("the quota read called %s %s, want GET on the project acme-prod", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		writeBody(w, &compute.Project{Name: "acme-prod", Quotas: quotas})
	}))
	t.Cleanup(served.Close)
	return pushing(t, served.URL)
}

func TestTheBackendServiceQuotaIsTheProjectsGlobalExternalManagedBackendServicesQuota(t *testing.T) {
	t.Parallel()

	p := quoting(t, []*compute.Quota{
		{Metric: "BACKEND_SERVICES", Usage: 3, Limit: 75},
		{Metric: "GLOBAL_EXTERNAL_MANAGED_BACKEND_SERVICES", Usage: 12, Limit: 50},
	})
	quota, found, err := p.ReadBackendServiceQuota(context.Background())
	if err != nil || !found {
		t.Fatalf("ReadBackendServiceQuota = %v, %v, %v, want the quota found", quota, found, err)
	}
	if want := (alb.BackendServiceQuota{Usage: 12, Limit: 50}); quota != want {
		t.Errorf("ReadBackendServiceQuota = %+v, want %+v: the alb's backend services are EXTERNAL_MANAGED and global", quota, want)
	}
}

func TestABackendServiceQuotaTheProjectDoesNotReportIsNotFound(t *testing.T) {
	t.Parallel()

	p := quoting(t, []*compute.Quota{{Metric: "BACKEND_SERVICES", Usage: 3, Limit: 75}})
	if _, found, err := p.ReadBackendServiceQuota(context.Background()); err != nil || found {
		t.Errorf("ReadBackendServiceQuota = found %v, %v, want not found and no error", found, err)
	}
}
