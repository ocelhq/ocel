package alb

import (
	"reflect"
	"testing"

	"github.com/ocelhq/ocel/platform/gcp/provider/edges/albprogram"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/albresources"
)

const (
	cdnBackend     = "ocel-alb-shop-production-shop-example-com"
	cdnCertificate = "projects/acme-prod/locations/global/certificates/shop"
)

func cdnBinding() albprogram.Program {
	return must(binding(map[string]albresources.BindingHost{"shop.example.com": {
		App: "web", Certificate: cdnCertificate, Service: "ocel-shop-prod-web", Backend: cdnBackend,
	}}))
}

func TestABindingsBackendKeysTheRSCVariantsNextServesOnOneURL(t *testing.T) {
	t.Parallel()

	seen, err := declared(cdnBinding())
	if err != nil {
		t.Fatalf("the binding program = %v", err)
	}

	got := cacheKeyPolicy(seen[cdnBackend])["includeHttpHeaders"]
	want := []any{"RSC", "Next-Router-Prefetch", "Next-Router-Segment-Prefetch", "Next-Url"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("includeHttpHeaders = %v, want %v; without them Cloud CDN refuses to cache a Next response that varies on them", got, want)
	}
}

func TestABindingsBackendKeepsHostProtocolAndQueryStringInItsKey(t *testing.T) {
	t.Parallel()

	seen, err := declared(cdnBinding())
	if err != nil {
		t.Fatalf("the binding program = %v", err)
	}

	key := cacheKeyPolicy(seen[cdnBackend])
	for _, field := range []string{"includeHost", "includeProtocol", "includeQueryString"} {
		if value, _ := key[field].(bool); !value {
			t.Errorf("cacheKeyPolicy.%s = %v, want true", field, key[field])
		}
	}
}

func TestADraftModeRequestIsCachedApartFromThePublishedPage(t *testing.T) {
	t.Parallel()

	seen, err := declared(cdnBinding())
	if err != nil {
		t.Fatalf("the binding program = %v", err)
	}

	got := cacheKeyPolicy(seen[cdnBackend])["includeNamedCookies"]
	want := []any{"__prerender_bypass"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("includeNamedCookies = %v, want %v; without it Cloud CDN answers a draft-mode request with the cached published page", got, want)
	}
}

func TestTheCacheKeyNamesNoMoreCookiesThanCloudCDNAllows(t *testing.T) {
	t.Parallel()

	seen, err := declared(cdnBinding())
	if err != nil {
		t.Fatalf("the binding program = %v", err)
	}

	cookies, ok := cacheKeyPolicy(seen[cdnBackend])["includeNamedCookies"].([]any)
	if !ok {
		t.Fatalf("includeNamedCookies is not a list")
	}
	if len(cookies) > 5 {
		t.Errorf("includeNamedCookies names %d cookies, Cloud CDN allows 5 (https://docs.cloud.google.com/cdn/docs/caching)", len(cookies))
	}
}

func TestABindingsBackendSendsARequestCarryingTheRevalidateHeaderToTheOrigin(t *testing.T) {
	t.Parallel()

	seen, err := declared(cdnBinding())
	if err != nil {
		t.Fatalf("the binding program = %v", err)
	}

	policy, _ := seen[cdnBackend].Args["cdnPolicy"].(map[string]any)
	got := policy["bypassCacheOnRequestHeaders"]
	want := []any{map[string]any{"headerName": "x-prerender-revalidate"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("bypassCacheOnRequestHeaders = %v, want %v; Cloud CDN answers the request from the cache otherwise", got, want)
	}
}

func cacheKeyPolicy(backend declaration) map[string]any {
	policy, ok := backend.Args["cdnPolicy"].(map[string]any)
	if !ok {
		return nil
	}
	key, _ := policy["cacheKeyPolicy"].(map[string]any)
	return key
}

func TestABindingsBackendCollapsesConcurrentMissesIntoOneOriginRequest(t *testing.T) {
	t.Parallel()

	seen, err := declared(cdnBinding())
	if err != nil {
		t.Fatalf("the binding program = %v", err)
	}

	policy, _ := seen[cdnBackend].Args["cdnPolicy"].(map[string]any)
	if got, _ := policy["requestCoalescing"].(bool); !got {
		t.Errorf("cdnPolicy.requestCoalescing = %v, want true: a stale page refresh reaches Cloud Run once, not once per visitor", policy["requestCoalescing"])
	}
}

func TestABindingsBackendServesStaleOnlyWhereTheOriginsStaleWhileRevalidateAllowsIt(t *testing.T) {
	t.Parallel()

	seen, err := declared(cdnBinding())
	if err != nil {
		t.Fatalf("the binding program = %v", err)
	}

	policy, _ := seen[cdnBackend].Args["cdnPolicy"].(map[string]any)
	got, present := policy["serveWhileStale"]
	if !present {
		t.Fatalf("cdnPolicy.serveWhileStale is unset, and Cloud CDN then serves a response without stale-while-revalidate stale for a day past its expiry")
	}
	if n, _ := got.(float64); n != 0 {
		t.Errorf("cdnPolicy.serveWhileStale = %v, want 0", got)
	}
}
