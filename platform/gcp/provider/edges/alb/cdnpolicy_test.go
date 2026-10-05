package alb

import (
	"reflect"
	"strings"
	"testing"
)

const (
	cdnBackend     = "ocel-alb-shop-production-shop-example-com"
	cdnCertificate = "projects/acme-prod/locations/global/certificates/shop"
)

func cdnBinding() Program {
	return binding(map[string]Host{"shop.example.com": {
		App: "web", Certificate: cdnCertificate, Service: "ocel-shop-prod-web", Backend: cdnBackend,
	}})
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

func TestNoKeyedHeaderIsOneCloudCDNRefusesToKey(t *testing.T) {
	t.Parallel()

	refused := []string{
		"accept", "accept-encoding", "authority", "authorization", "cdn-loop", "connection",
		"content-md5", "content-type", "cookie", "date", "forwarded", "from", "host",
		"if-match", "if-modified-since", "if-none-match", "origin", "proxy-authorization",
		"range", "referer", "referrer", "user-agent", "want-digest", "x-csrftoken",
		"x-csrf-token", "x-forwarded-for", "x-user-ip",
	}
	prefixes := []string{
		"access-control-", "sec-fetch-", "sec-gfe-", "sec-google-", "x-amz-", "x-gfe-", "x-goog-", "x-google-",
	}

	for _, header := range cacheKeyHeaders {
		name := strings.ToLower(header)
		for _, bad := range refused {
			if name == bad {
				t.Errorf("cacheKeyHeaders holds %s, which Cloud CDN refuses (https://docs.cloud.google.com/cdn/docs/caching)", header)
			}
		}
		for _, prefix := range prefixes {
			if strings.HasPrefix(name, prefix) {
				t.Errorf("cacheKeyHeaders holds %s, whose %s prefix Cloud CDN refuses (https://docs.cloud.google.com/cdn/docs/caching)", header, prefix)
			}
		}
	}
}

func TestAShieldedBindingsBackendKeysNoHeaderOrCookieSoNextPagesStayUncachedThere(t *testing.T) {
	t.Parallel()

	seen, err := declared(shieldedBinding(map[string]Host{"shop.example.com": {
		App: "web", Certificate: cdnCertificate, Service: "ocel-shop-prod-web", Backend: cdnBackend,
	}}, true))
	if err != nil {
		t.Fatalf("the binding program = %v", err)
	}

	key := cacheKeyPolicy(seen[cdnBackend])
	for _, field := range []string{"includeHttpHeaders", "includeNamedCookies"} {
		if named, _ := key[field].([]any); len(named) > 0 {
			t.Errorf("cacheKeyPolicy.%s = %v, want none: nothing purges a shielded load balancer's Cloud CDN, so a cacheable Next page would outlive revalidateTag", field, named)
		}
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
