package gcp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
)

func TestTheAssetsBucketTheLoadBalancerServesIsTheTiersBucketReadByItsServiceAgent(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"projectId":"acme-prod","projectNumber":"123456789"}`))
	}))
	t.Cleanup(server.Close)

	got, err := pushing(t, server.URL).readAssetsBucket(context.Background(), environment.TierProduction)
	if err != nil {
		t.Fatalf("readAssetsBucket = %v", err)
	}
	if want := "ocel-acme-prod-production"; got.Name != want {
		t.Errorf("the bucket is %q, want %q: the tier's one bucket holds its releases' static files", got.Name, want)
	}
	if want := "serviceAccount:service-123456789@https-lb.iam.gserviceaccount.com"; got.Agent != want {
		t.Errorf("the reader is %q, want %q", got.Agent, want)
	}
}
