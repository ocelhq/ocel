package site

import (
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/platform/aws/provider/payloads"
)

type Buckets struct {
	naming.Buckets
	Objects      payloads.ObjectStore
	Tier         environment.Tier
	EdgeHoldsKey bool
}

func (b Buckets) CanStoreAssets() bool {
	return b.Holding(naming.ObjectAssets) != "" && b.Objects != nil
}

func (b Buckets) CanEdgeRead() bool {
	return b.CanStoreAssets() && b.EdgeHoldsKey
}

func (b Buckets) Find(key string) (string, error) {
	kind, err := naming.ParseObjectKind(key)
	if err != nil {
		return "", err
	}
	return b.Holding(kind), nil
}

func (b Buckets) target(kind naming.ObjectKind) uploadTarget {
	return uploadTarget{up: b.Objects, bucket: b.Holding(kind), kind: kind, tier: b.Tier}
}
