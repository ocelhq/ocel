package deploy

import (
	"testing"

	"github.com/ocelhq/ocel/pkg/provider"
)

func TestAServerlessAppUsingProvisionedPostgresIsBilledForTheTiersNATGateway(t *testing.T) {
	t.Parallel()

	set := shaped(t, nil, shapeRequest(t))

	scopes := map[string]string{}
	for _, r := range set.GetResources() {
		scopes[r.GetType()+":"+r.GetName()] = r.GetScope()
	}
	for _, key := range []string{"aws_nat_gateway:" + FunctionNetworkSlug, "aws_eip:" + FunctionNetworkSlug} {
		if scopes[key] != "project:shop/shared:production" {
			t.Errorf("%s is in scope %q, want the tier's shared scope: one gateway serves every app in it", key, scopes[key])
		}
	}
}

func TestOnlyAServerlessAppReachingAVPCOnlyResourceOcelProvisionsPaysForTheNATGateway(t *testing.T) {
	t.Parallel()

	postgres := provider.Resource{Name: "main", Type: provider.BindingPostgres, Postgres: &provider.PostgresSpec{}}
	cache := provider.Resource{Name: "cache", Type: provider.BindingKV, KV: &provider.KVSpec{}}
	consumed := provider.Resource{Name: "main", Type: provider.BindingPostgres, Binding: "elsewhere"}
	bucket := provider.Resource{Name: "uploads", Type: provider.BindingBucket, Bucket: &provider.BucketSpec{}}
	cases := []struct {
		name   string
		usages []provider.AppUsage
		want   int
	}{
		{"no app uses anything", nil, 0},
		{"a serverless app uses postgres", []provider.AppUsage{{App: "web", Resources: []provider.Resource{postgres}}}, 1},
		{"a serverless app uses kv", []provider.AppUsage{{App: "web", Resources: []provider.Resource{cache}}}, 1},
		{"two serverless apps share the one gateway", []provider.AppUsage{{App: "web", Resources: []provider.Resource{postgres}}, {App: "other", Resources: []provider.Resource{cache}}}, 1},
		{"a serverless app uses only a bucket", []provider.AppUsage{{App: "web", Resources: []provider.Resource{bucket}}}, 0},
		{"a container app uses postgres", []provider.AppUsage{{App: "api", Resources: []provider.Resource{postgres}}}, 0},
		{"a serverless app uses postgres another project provisions", []provider.AppUsage{{App: "web", Resources: []provider.Resource{consumed}}}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			req := shapeRequest(t)
			req.Usages = tc.usages
			set := shaped(t, nil, req)

			counts := countTypes(set)
			if counts["aws_nat_gateway"] != tc.want || counts["aws_eip"] != tc.want {
				t.Errorf("shaped %d NAT gateways and %d elastic IPs, want %d of each", counts["aws_nat_gateway"], counts["aws_eip"], tc.want)
			}
		})
	}
}
