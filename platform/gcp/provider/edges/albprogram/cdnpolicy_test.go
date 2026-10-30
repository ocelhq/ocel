package albprogram

import (
	"strings"
	"testing"
)

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
