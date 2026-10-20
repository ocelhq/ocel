package googletest

import (
	"context"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/names"
)

const (
	Project   = "acme-prod"
	Region    = "europe-west1"
	BucketTag = "k3x9q2"
)

var Names = names.New(provider.Namespace("ocel"), Project)

func TierBuckets(tier environment.Tier) names.TierBuckets {
	var buckets names.TierBuckets
	for _, class := range names.TierStorageClasses {
		buckets = buckets.With(class, Names.TierBucket(tier, class, BucketTag))
	}
	return buckets
}

func WithTierBuckets(c *gcloud.Clients) *gcloud.Clients {
	for _, tier := range []environment.Tier{environment.TierProduction, environment.TierPreview} {
		c.RememberTierBuckets(tier, TierBuckets(tier))
	}
	return c
}

func Clients(endpoint string) *gcloud.Clients {
	return WithTierBuckets(&gcloud.Clients{Names: Names, Region: Region, Endpoint: endpoint})
}

func Opener(clients *gcloud.Clients) func(context.Context) (*gcloud.Clients, error) {
	return func(context.Context) (*gcloud.Clients, error) { return clients, nil }
}
