package alb

import (
	"github.com/pulumi/pulumi-gcp/sdk/v9/go/gcp/compute"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

var cacheKeyHeaders = []string{"RSC", "Next-Router-Prefetch", "Next-Router-Segment-Prefetch", "Next-Url"}

const draftModeCookie = "__prerender_bypass"

func newCDNPolicy(shielded bool) *compute.BackendServiceCdnPolicyArgs {
	key := &compute.BackendServiceCdnPolicyCacheKeyPolicyArgs{
		IncludeHost:        pulumi.Bool(true),
		IncludeProtocol:    pulumi.Bool(true),
		IncludeQueryString: pulumi.Bool(true),
	}
	if !shielded {
		key.IncludeHttpHeaders = pulumi.ToStringArray(cacheKeyHeaders)
		key.IncludeNamedCookies = pulumi.StringArray{pulumi.String(draftModeCookie)}
	}
	return &compute.BackendServiceCdnPolicyArgs{
		CacheMode:      pulumi.String(cacheOnOrigin),
		CacheKeyPolicy: key,
	}
}
