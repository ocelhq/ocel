package names

import (
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
)

var TierStorageClasses = []naming.StorageClass{naming.StorageState, naming.StorageEdge, naming.StorageCache}

func (n Names) TierBucketStem(tier environment.Tier, class naming.StorageClass) string {
	return n.ocelName(tier, string(class))
}

func (n Names) TierBucket(tier environment.Tier, class naming.StorageClass, tag string) string {
	return n.TierBucketStem(tier, class) + naming.WordSeparator + tag
}
