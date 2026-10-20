package names

import (
	"strings"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
)

var TierStorageClasses = []naming.StorageClass{naming.StorageState, naming.StorageEdge, naming.StorageCache}

type TierBuckets struct {
	State string
	Edge  string
	Cache string
}

func (b TierBuckets) Of(class naming.StorageClass) string {
	switch class {
	case naming.StorageState:
		return b.State
	case naming.StorageEdge:
		return b.Edge
	case naming.StorageCache:
		return b.Cache
	}
	return ""
}

func (b TierBuckets) With(class naming.StorageClass, bucket string) TierBuckets {
	switch class {
	case naming.StorageState:
		b.State = bucket
	case naming.StorageEdge:
		b.Edge = bucket
	case naming.StorageCache:
		b.Cache = bucket
	}
	return b
}

func (b TierBuckets) Missing() []naming.StorageClass {
	var missing []naming.StorageClass
	for _, class := range TierStorageClasses {
		if b.Of(class) == "" {
			missing = append(missing, class)
		}
	}
	return missing
}

func (n Names) TierBucketPrefix(tier environment.Tier) string {
	return n.mustOcelName(naming.TierWord(tier)) + naming.WordSeparator
}

func (n Names) UntaggedTierBucket(tier environment.Tier, class naming.StorageClass) string {
	return n.mustOcelName(naming.TierWord(tier), string(class))
}

func (n Names) TierBucket(tier environment.Tier, class naming.StorageClass, tag string) string {
	return n.UntaggedTierBucket(tier, class) + naming.WordSeparator + tag
}

func (n Names) TierBucketClass(tier environment.Tier, bucket string) (naming.StorageClass, bool) {
	for _, class := range TierStorageClasses {
		tag, found := strings.CutPrefix(bucket, n.UntaggedTierBucket(tier, class)+naming.WordSeparator)
		if found && naming.IsRandomTag(tag) {
			return class, true
		}
	}
	return "", false
}
