package alb

import (
	"github.com/pulumi/pulumi-gcp/sdk/v9/go/gcp/compute"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

var cacheKeyHeaders = []string{"RSC", "Next-Router-Prefetch", "Next-Router-Segment-Prefetch", "Next-Url"}

func newCDNPolicy() *compute.BackendServiceCdnPolicyArgs {
	return &compute.BackendServiceCdnPolicyArgs{
		CacheMode: pulumi.String(cacheOnOrigin),
		CacheKeyPolicy: &compute.BackendServiceCdnPolicyCacheKeyPolicyArgs{
			IncludeHost:        pulumi.Bool(true),
			IncludeProtocol:    pulumi.Bool(true),
			IncludeQueryString: pulumi.Bool(true),
			IncludeHttpHeaders: pulumi.ToStringArray(cacheKeyHeaders),
		},
	}
}
