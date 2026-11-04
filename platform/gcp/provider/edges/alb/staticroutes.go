package alb

import (
	"encoding/json"
	"fmt"

	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/router"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/albresources"
)

func staticRoutesOf(record router.ReleaseRecord, balancer LoadBalancer) (*albresources.StaticRoutes, error) {
	if balancer.AssetsBackend == "" || !hasUploadedStaticFiles(record) ||
		record.Static == nil || len(record.Static.ImmutablePrefixes) == 0 {
		return nil, nil
	}
	storage, err := record.ParseStorageCoordinate()
	if err != nil {
		return nil, err
	}
	return &albresources.StaticRoutes{
		Backend:    balancer.AssetsBackend,
		Objects:    naming.PathSeparator + storage.StoragePrefix(naming.ObjectAssets),
		Immutable:  record.Static.ImmutablePrefixes,
		Revalidate: record.Static.MustRevalidatePrefixes,
	}, nil
}

func hasUploadedStaticFiles(record router.ReleaseRecord) bool {
	return record.Framework == buildoutput.FrameworkNext && record.Physical == ""
}

func encodeStaticRoutes(record router.ReleaseRecord, balancer LoadBalancer) (string, error) {
	routes, err := staticRoutesOf(record, balancer)
	if err != nil || routes == nil {
		return "", err
	}
	encoded, err := json.Marshal(routes)
	if err != nil {
		return "", fmt.Errorf("encode the static routes of release %s of %s: %w", record.Release, record.App, err)
	}
	return string(encoded), nil
}

func (h Host) decodeStaticRoutes() (*albresources.StaticRoutes, error) {
	if h.StaticRoutes == "" {
		return nil, nil
	}
	var routes albresources.StaticRoutes
	if err := json.Unmarshal([]byte(h.StaticRoutes), &routes); err != nil {
		return nil, fmt.Errorf("decode the static routes recorded for a host of %s: %w", h.App, err)
	}
	return &routes, nil
}
