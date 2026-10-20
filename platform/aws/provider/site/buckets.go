package site

import (
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/platform/aws/provider/payloads"
)

type Buckets struct {
	Objects      payloads.ObjectStore
	Artifacts    string
	Edge         string
	Cache        string
	Tier         environment.Tier
	EdgeHoldsKey bool
}

func (b Buckets) CanStoreAssets() bool {
	return b.Edge != "" && b.Objects != nil
}

func (b Buckets) CanEdgeRead() bool {
	return b.CanStoreAssets() && b.EdgeHoldsKey
}

func (b Buckets) Find(key string) (string, error) {
	kind, err := naming.ParseObjectKind(key)
	if err != nil {
		return "", err
	}
	return b.holding(kind.Class()), nil
}

func (b Buckets) holding(class naming.StorageClass) string {
	switch class {
	case naming.StorageArtifacts:
		return b.Artifacts
	case naming.StorageEdge:
		return b.Edge
	case naming.StorageCache:
		return b.Cache
	}
	return ""
}

func (b Buckets) target(kind naming.ObjectKind) uploadTarget {
	return uploadTarget{up: b.Objects, bucket: b.holding(kind.Class()), kind: kind, tier: b.Tier}
}
