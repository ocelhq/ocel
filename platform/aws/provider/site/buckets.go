package site

import (
	"fmt"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
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

func (b Buckets) Find(store string) (string, error) {
	switch store {
	case provider.StoreFunctions:
		return b.Artifacts, nil
	case provider.StoreAssets:
		return b.Assets, nil
	}
	return "", fmt.Errorf("this provider keeps no %q store", store)
}
