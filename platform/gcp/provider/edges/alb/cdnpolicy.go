package alb

import (
	"github.com/pulumi/pulumi-gcp/sdk/v9/go/gcp/compute"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

var cacheKeyHeaders = []string{"RSC", "Next-Router-Prefetch", "Next-Router-Segment-Prefetch", "Next-Url"}

const draftModeCookie = "__prerender_bypass"

const revalidateHeader = "x-prerender-revalidate"

func newCDNPolicy() *compute.BackendServiceCdnPolicyArgs {
	key := &compute.BackendServiceCdnPolicyCacheKeyPolicyArgs{
		IncludeHost:         pulumi.Bool(true),
		IncludeProtocol:     pulumi.Bool(true),
		IncludeQueryString:  pulumi.Bool(true),
		IncludeHttpHeaders:  pulumi.ToStringArray(cacheKeyHeaders),
		IncludeNamedCookies: pulumi.StringArray{pulumi.String(draftModeCookie)},
	}
	return &compute.BackendServiceCdnPolicyArgs{
		CacheMode:         pulumi.String(cacheOnOrigin),
		RequestCoalescing: pulumi.Bool(true),
		ServeWhileStale:   pulumi.Int(0),
		CacheKeyPolicy:    key,
		BypassCacheOnRequestHeaders: compute.BackendServiceCdnPolicyBypassCacheOnRequestHeaderArray{
			compute.BackendServiceCdnPolicyBypassCacheOnRequestHeaderArgs{HeaderName: pulumi.String(revalidateHeader)},
		},
	}
}
