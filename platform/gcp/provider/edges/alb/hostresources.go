package alb

import (
	"fmt"
	"maps"
	"slices"

	"github.com/ocelhq/ocel/pkg/edge"
)

type HostResources struct {
	Hostname             string
	CertificateMapEntry  string
	NetworkEndpointGroup string
	BackendService       string
}

func ReadHostResources(namespace string, state edge.StackState) ([]HostResources, error) {
	var recorded edgeRecord
	if err := state.Private.Into(&recorded); err != nil {
		return nil, fmt.Errorf("read the hosts the %s edge binds for %s: %w", Kind, state.Slug, err)
	}
	listed := make([]HostResources, 0, len(recorded.Hosts))
	for _, hostname := range slices.Sorted(maps.Keys(recorded.Hosts)) {
		host := recorded.Hosts[hostname]
		held := HostResources{Hostname: hostname}
		var err error
		if host.Certificate != "" {
			if held.CertificateMapEntry, err = entryName(namespace, state.Slug, state.Tier, hostname); err != nil {
				return nil, err
			}
		}
		if host.Service != "" {
			if held.NetworkEndpointGroup, err = negName(namespace, state.Slug, state.Tier, hostname); err != nil {
				return nil, err
			}
			held.BackendService = host.Backend
		}
		listed = append(listed, held)
	}
	return listed, nil
}
