package alb

import (
	"reflect"
	"strings"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/environment"
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

func TestThePreviewWildcardBackendKeysWhatABindingsBackendKeys(t *testing.T) {
	t.Parallel()

	bound, err := declared(cdnBinding())
	if err != nil {
		t.Fatalf("the binding program = %v", err)
	}
	preview, err := declared(func(ctx *pulumi.Context, project string) error {
		return previewWildcardResources(ctx, loadBalancerSpec{
			Region:  "europe-west1",
			Names:   loadBalancerNames(environment.TierPreview, false),
			Preview: previewEntry{BaseDomain: previewBase, Certificate: previewCertificate},
		}, project)
	})
	if err != nil {
		t.Fatalf("previewWildcardResources = %v", err)
	}

	want := cacheKeyPolicy(bound[cdnBackend])
	got := cacheKeyPolicy(preview[previewBackendName(previewBase)])
	if want == nil || !reflect.DeepEqual(got, want) {
		t.Errorf("preview wildcard cacheKeyPolicy = %v, want the binding's %v", got, want)
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
