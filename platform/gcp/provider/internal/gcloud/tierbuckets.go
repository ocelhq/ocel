package gcloud

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"cloud.google.com/go/storage"
	"google.golang.org/api/iterator"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/names"
)

type tierBucketsFound struct {
	mu    sync.Mutex
	found map[environment.Tier]names.TierBuckets
}

func (c *Clients) TierBuckets(ctx context.Context, tier environment.Tier) (names.TierBuckets, error) {
	c.tierBuckets.mu.Lock()
	defer c.tierBuckets.mu.Unlock()
	if found, ok := c.tierBuckets.found[tier]; ok {
		return found, nil
	}
	listed, err := c.listBuckets(ctx, c.TierBucketPrefix(tier))
	if err != nil {
		return names.TierBuckets{}, err
	}
	byClass := map[naming.StorageClass][]*storage.BucketAttrs{}
	for _, attrs := range listed {
		if class, ok := c.TierBucketClass(tier, attrs.Name); ok {
			byClass[class] = append(byClass[class], attrs)
		}
	}
	var found names.TierBuckets
	for _, class := range names.TierStorageClasses {
		one, err := c.soleTierBucket(tier, class, byClass[class])
		if err != nil {
			return names.TierBuckets{}, err
		}
		if one != nil {
			found = found.With(class, one.Name)
		}
	}
	if missing := found.Missing(); len(missing) > 0 {
		return names.TierBuckets{}, refusal.Refuse(refusal.CodeNotReady,
			"Google Cloud project %s holds no %s bucket of the %s tier, so the tier is not bootstrapped.\nRun `%s` to create it, then try again",
			c.Project(), missing[0], tier, provider.BootstrapCommand(tier))
	}
	c.rememberTierBuckets(tier, found)
	return found, nil
}

func (c *Clients) RememberTierBuckets(tier environment.Tier, buckets names.TierBuckets) {
	c.tierBuckets.mu.Lock()
	defer c.tierBuckets.mu.Unlock()
	c.rememberTierBuckets(tier, buckets)
}

func (c *Clients) ForgetTierBuckets(tier environment.Tier) {
	c.tierBuckets.mu.Lock()
	defer c.tierBuckets.mu.Unlock()
	delete(c.tierBuckets.found, tier)
}

func (c *Clients) rememberTierBuckets(tier environment.Tier, buckets names.TierBuckets) {
	if c.tierBuckets.found == nil {
		c.tierBuckets.found = map[environment.Tier]names.TierBuckets{}
	}
	c.tierBuckets.found[tier] = buckets
}

func (c *Clients) FindTierBucket(ctx context.Context, tier environment.Tier, class naming.StorageClass) (*storage.BucketAttrs, error) {
	matching, err := c.ListTierBucketsOf(ctx, tier, class)
	if err != nil {
		return nil, err
	}
	return c.soleTierBucket(tier, class, matching)
}

func (c *Clients) ListTierBucketsOf(ctx context.Context, tier environment.Tier, class naming.StorageClass) ([]*storage.BucketAttrs, error) {
	listed, err := c.listBuckets(ctx, c.UntaggedTierBucket(tier, class)+naming.WordSeparator)
	if err != nil {
		return nil, err
	}
	var matching []*storage.BucketAttrs
	for _, attrs := range listed {
		if found, ok := c.TierBucketClass(tier, attrs.Name); ok && found == class {
			matching = append(matching, attrs)
		}
	}
	return matching, nil
}

func (c *Clients) soleTierBucket(tier environment.Tier, class naming.StorageClass, matching []*storage.BucketAttrs) (*storage.BucketAttrs, error) {
	switch len(matching) {
	case 0:
		return nil, nil
	case 1:
		return matching[0], nil
	}
	named := make([]string, len(matching))
	for i, attrs := range matching {
		named[i] = attrs.Name
	}
	return nil, refusal.Refuse(refusal.CodeInvalid,
		"the %s tier keeps its %s objects in %d Cloud Storage buckets, %s, and a tier keeps them in one.\n"+
			"Delete the one that holds nothing you need with `gcloud storage rm --recursive gs://<bucket>`, then try again",
		tier, class, len(named), strings.Join(named, ", "))
}

func (c *Clients) listBuckets(ctx context.Context, prefix string) ([]*storage.BucketAttrs, error) {
	client, err := c.Storage()
	if err != nil {
		return nil, err
	}
	listed := client.Buckets(ctx, c.Project())
	listed.Prefix = prefix
	var found []*storage.BucketAttrs
	for {
		attrs, err := listed.Next()
		if errors.Is(err, iterator.Done) {
			return found, nil
		}
		if err != nil {
			return nil, fmt.Errorf("list the Cloud Storage buckets of project %s named %s…: %w", c.Project(), prefix, err)
		}
		found = append(found, attrs)
	}
}
