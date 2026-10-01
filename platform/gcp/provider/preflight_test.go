package gcp

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
)

func withStamp(t *testing.T, written stamp) *Provider {
	t.Helper()
	served := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || !strings.HasSuffix(r.URL.Path, "/ocel-acme-prod-production/"+StampObject) {
			t.Errorf("preflight called %s %s, want only the tier's bootstrap stamp read", r.Method, r.URL.Path)
		}
		writeBody(w, written)
	}))
	t.Cleanup(served.Close)
	return pushing(t, served.URL)
}

func declaringAStore(memory int64) provider.DeployPreflight {
	return provider.DeployPreflight{
		Deploy:    provider.DeploySpec{Tier: environment.TierProduction},
		Resources: []provider.Resource{{Name: "cache", Type: provider.BindingKV, KV: &provider.KVSpec{MemoryBytes: memory}}},
	}
}

func TestADeployDeclaringAStoreIsAdmittedOnceTheTierHasItsNetwork(t *testing.T) {
	if err := withStamp(t, stamp{State: stateComplete, Features: []string{kvFeature}}).PreflightDeploy(context.Background(), declaringAStore(256<<20)); err != nil {
		t.Errorf("PreflightDeploy() = %v, want a store admitted on a tier whose network is installed", err)
	}
}

func TestADeployDeclaringAStoreIsRefusedUntilTheTierHasItsNetwork(t *testing.T) {
	for _, written := range []stamp{
		{State: stateComplete},
		{State: stateApplying, Features: []string{kvFeature}},
	} {
		err := withStamp(t, written).PreflightDeploy(context.Background(), declaringAStore(256<<20))
		var refused refusal.Refusal
		if !errors.As(err, &refused) || refused.Code != refusal.CodeNotReady ||
			!strings.Contains(err.Error(), "ocel bootstrap production --features "+kvFeature) {
			t.Errorf("PreflightDeploy() under stamp %+v = %v, want it refused naming the bootstrap that installs the network", written, err)
		}
	}
}

func TestADeployDeclaringAStoreNoNodeHoldsIsRefusedBeforeAnythingIsRead(t *testing.T) {
	served := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		t.Errorf("preflight called %s %s before refusing a store no node holds", r.Method, r.URL.Path)
	}))
	t.Cleanup(served.Close)

	err := pushing(t, served.URL).PreflightDeploy(context.Background(), declaringAStore(64<<30))
	if err == nil || !strings.Contains(err.Error(), "highmem-xlarge") {
		t.Errorf("PreflightDeploy() = %v, want the store refused naming the largest node", err)
	}
}

func TestADeployDeclaringNoStoreReadsNothing(t *testing.T) {
	served := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		t.Errorf("preflight called %s %s for a deploy declaring no store", r.Method, r.URL.Path)
	}))
	t.Cleanup(served.Close)

	if err := pushing(t, served.URL).PreflightDeploy(context.Background(), provider.DeployPreflight{}); err != nil {
		t.Errorf("PreflightDeploy() = %v", err)
	}
}
