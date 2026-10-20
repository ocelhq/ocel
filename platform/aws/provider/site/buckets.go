package site

import (
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/platform/aws/provider/payloads"
)

type Buckets struct {
	Objects      payloads.ObjectStore
	Assets       string
	Artifacts    string
	Tier         environment.Tier
	EdgeHoldsKey bool
}

func (b Buckets) CanStoreAssets() bool {
	return b.Assets != "" && b.Objects != nil
}

func (b Buckets) CanEdgeRead() bool {
	return b.CanStoreAssets() && b.EdgeHoldsKey
}

func (b Buckets) Find(key string) (string, error) {
	kind, err := naming.ParseObjectKind(key)
	if err != nil {
		return "", err
	}
	if kind == naming.ObjectFunction || kind == naming.ObjectPayloads {
		return b.Artifacts, nil
	}
	return b.Assets, nil
}
