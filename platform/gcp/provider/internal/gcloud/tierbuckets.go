package gcloud

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"sync"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/refusal"
)

const RootTierBuckets records.Root = "tierbuckets"

const tierBucketsEntry = "names"

func TierBucketsKey(tier environment.Tier) records.Key {
	return records.Partition{Tier: tier, Root: RootTierBuckets}.Key(tierBucketsEntry)
}

type tierBucketsRead struct {
	mu   sync.Mutex
	read map[environment.Tier]naming.Buckets
}

func (c *Clients) RecordedTierBuckets(ctx context.Context, tier environment.Tier) (naming.Buckets, error) {
	c.tierBuckets.mu.Lock()
	defer c.tierBuckets.mu.Unlock()
	if read, ok := c.tierBuckets.read[tier]; ok {
		return maps.Clone(read), nil
	}
	store, err := c.tierBucketRecords(tier)
	if err != nil {
		return nil, err
	}
	entry, err := records.ReadOrEmpty(ctx, store, TierBucketsKey(tier))
	if err != nil {
		return nil, fmt.Errorf("read the buckets the %s tier keeps its objects in: %w", tier, err)
	}
	read, err := decodeTierBuckets(entry)
	if err != nil {
		return nil, err
	}
	if c.tierBuckets.read == nil {
		c.tierBuckets.read = map[environment.Tier]naming.Buckets{}
	}
	c.tierBuckets.read[tier] = read
	return maps.Clone(read), nil
}

func (c *Clients) TierBuckets(ctx context.Context, tier environment.Tier, classes ...naming.StorageClass) (naming.Buckets, error) {
	recorded, err := c.RecordedTierBuckets(ctx, tier)
	if err != nil {
		return nil, err
	}
	for _, class := range classes {
		if recorded[class] == "" {
			c.DropTierBucketsRead(tier)
			return nil, refusal.Refuse(refusal.CodeNotReady,
				"Google Cloud project %s records no %s bucket of the %s tier, so the tier is not bootstrapped.\nRun `%s` to create it, then try again",
				c.Project(), class, tier, provider.BootstrapCommand(tier))
		}
	}
	return recorded, nil
}

func (c *Clients) ClaimTierBucket(ctx context.Context, tier environment.Tier, class naming.StorageClass, mint func() string) (string, error) {
	recorded, err := c.changeTierBuckets(ctx, tier, func(buckets naming.Buckets) bool {
		if buckets[class] != "" {
			return false
		}
		buckets[class] = mint()
		return true
	})
	if err != nil {
		return "", fmt.Errorf("record the %s bucket of the %s tier: %w", class, tier, err)
	}
	return recorded[class], nil
}

func (c *Clients) ForgetTierBucket(ctx context.Context, tier environment.Tier, class naming.StorageClass, bucket string) error {
	_, err := c.changeTierBuckets(ctx, tier, func(buckets naming.Buckets) bool {
		if buckets[class] != bucket {
			return false
		}
		delete(buckets, class)
		return true
	})
	if err != nil {
		return fmt.Errorf("forget the %s bucket %s of the %s tier: %w", class, bucket, tier, err)
	}
	return nil
}

func (c *Clients) changeTierBuckets(ctx context.Context, tier environment.Tier, change func(naming.Buckets) bool) (naming.Buckets, error) {
	store, err := c.tierBucketRecords(tier)
	if err != nil {
		return nil, err
	}
	var recorded naming.Buckets
	err = records.Change(ctx, store, TierBucketsKey(tier), func(entry records.Entry) ([]byte, bool, error) {
		var err error
		if recorded, err = decodeTierBuckets(entry); err != nil || !change(recorded) {
			return nil, false, err
		}
		value, err := json.Marshal(recorded)
		return value, err == nil, err
	})
	if err != nil {
		c.DropTierBucketsRead(tier)
		return nil, err
	}
	c.tierBuckets.mu.Lock()
	defer c.tierBuckets.mu.Unlock()
	if c.tierBuckets.read == nil {
		c.tierBuckets.read = map[environment.Tier]naming.Buckets{}
	}
	c.tierBuckets.read[tier] = maps.Clone(recorded)
	return recorded, nil
}

func (c *Clients) tierBucketRecords(tier environment.Tier) (records.Store, error) {
	if c.Records == nil {
		return nil, fmt.Errorf("gcp: these clients hold no record store, so the buckets of the %s tier cannot be read or recorded", tier)
	}
	return c.Records, nil
}

func (c *Clients) DropTierBucketsRead(tier environment.Tier) {
	c.tierBuckets.mu.Lock()
	defer c.tierBuckets.mu.Unlock()
	delete(c.tierBuckets.read, tier)
}

func decodeTierBuckets(entry records.Entry) (naming.Buckets, error) {
	buckets := naming.Buckets{}
	if len(entry.Value) == 0 {
		return buckets, nil
	}
	if err := json.Unmarshal(entry.Value, &buckets); err != nil {
		return nil, fmt.Errorf("read %s: %w", entry.Key, err)
	}
	return buckets, nil
}
