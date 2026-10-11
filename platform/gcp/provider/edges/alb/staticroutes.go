package alb

import (
	"encoding/json"

	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/router"
)

type StaticRoutes struct {
	Backend    string   `json:"backend"`
	Objects    string   `json:"objects"`
	Immutable  []string `json:"immutable"`
	Revalidate []string `json:"revalidate,omitempty"`
}

func staticRoutesOf(record router.ReleaseRecord, balancer LoadBalancer) *StaticRoutes {
	if balancer.Shielded || balancer.AssetsBackend == "" || record.Framework != buildoutput.FrameworkNext ||
		record.AssetPrefix == "" || record.Static == nil || len(record.Static.ImmutablePrefixes) == 0 {
		return nil
	}
	return &StaticRoutes{
		Backend:    balancer.AssetsBackend,
		Objects:    "/" + provider.StoreAssets + "/" + record.AssetPrefix,
		Immutable:  record.Static.ImmutablePrefixes,
		Revalidate: record.Static.MustRevalidatePrefixes,
	}
}

func staticOf(record router.ReleaseRecord, balancer LoadBalancer) string {
	files := staticRoutesOf(record, balancer)
	if files == nil {
		return ""
	}
	encoded, _ := json.Marshal(files)
	return string(encoded)
}

func (h Host) files() *StaticRoutes {
	if h.Static == "" {
		return nil
	}
	var files StaticRoutes
	if err := json.Unmarshal([]byte(h.Static), &files); err != nil {
		return nil
	}
	return &files
}
