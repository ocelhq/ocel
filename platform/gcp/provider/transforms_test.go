package gcp

import (
	"context"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/resources"
)

func TestATransformedProviderRefusesAResourceItsTransformsWereNotEvaluatedFor(t *testing.T) {
	p := pushing(t, "")
	p.transforms = &patching{}
	hooks := p.resourceHooks()
	ref := provider.StackRef{Project: "shop", Tier: environment.TierProduction, Name: naming.InfraStack("prod")}
	for _, each := range []struct {
		hooks    *resources.ResourceHooks
		resource provider.Resource
	}{
		{hooks.KV, provider.Resource{Name: "cache", Type: provider.BindingKV, KV: &provider.KVSpec{MemoryBytes: 256 << 20}}},
		{hooks.Postgres, provider.Resource{Name: "orders", Type: provider.BindingPostgres, Postgres: &provider.PostgresSpec{Version: "17"}}},
		{hooks.Bucket, provider.Resource{Name: "uploads", Type: provider.BindingBucket, Bucket: &provider.BucketSpec{}}},
		{hooks.Topic, provider.Resource{Name: "orders", Declared: "orders", Type: provider.BindingTopic, Topic: &provider.TopicSpec{}}},
	} {
		t.Run(string(each.resource.Type), func(t *testing.T) {
			in := resources.ProvisionRequest{Ref: ref, Resource: each.resource}
			_, _, planned := each.hooks.Plan(context.Background(), in, nil)
			_, provisioned := each.hooks.Provision(context.Background(), in, nil)
			for _, err := range []error{planned, provisioned} {
				if err == nil || !strings.Contains(err.Error(), "were not evaluated") {
					t.Errorf("reaching %s %s with no evaluated transforms = %v, want it refused before it is left unpatched", each.resource.Type, each.resource.Name, err)
				}
			}
		})
	}
}
