package alb

import (
	"encoding/json"
	"fmt"

	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/router"
)

type StaticRoutes struct {
	Backend    string   `json:"backend"`
	Objects    string   `json:"objects"`
	Immutable  []string `json:"immutable"`
	Revalidate []string `json:"revalidate,omitempty"`
}

func staticRoutesOf(record router.ReleaseRecord, balancer LoadBalancer) *StaticRoutes {
	if balancer.Shielded || balancer.AssetsBackend == "" || !hasUploadedStaticFiles(record) ||
		record.Static == nil || len(record.Static.ImmutablePrefixes) == 0 {
		return nil
	}
	storage, err := naming.ParseStorageCoordinate(record.StorageCoordinate)
	if err != nil {
		return nil
	}
	return &StaticRoutes{
		Backend:    balancer.AssetsBackend,
		Objects:    naming.PathSeparator + storage.StoragePrefix(naming.ObjectAssets),
		Immutable:  record.Static.ImmutablePrefixes,
		Revalidate: record.Static.MustRevalidatePrefixes,
	}
}

func hasUploadedStaticFiles(record router.ReleaseRecord) bool {
	return record.Framework == buildoutput.FrameworkNext && record.Physical == ""
}

func encodeStaticRoutes(record router.ReleaseRecord, balancer LoadBalancer) (string, error) {
	routes := staticRoutesOf(record, balancer)
	if routes == nil {
		return "", nil
	}
	encoded, err := json.Marshal(routes)
	if err != nil {
		return "", fmt.Errorf("encode the static routes of release %s of %s: %w", record.Release, record.App, err)
	}
	return string(encoded), nil
}

func (h Host) decodeStaticRoutes() (*StaticRoutes, error) {
	if h.StaticRoutes == "" {
		return nil, nil
	}
	var routes StaticRoutes
	if err := json.Unmarshal([]byte(h.StaticRoutes), &routes); err != nil {
		return nil, fmt.Errorf("decode the static routes recorded for a host of %s: %w", h.App, err)
	}
	return &routes, nil
}
