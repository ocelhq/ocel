package alb

import (
	"maps"
	"slices"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/certs"
	"github.com/ocelhq/ocel/platform/aws/provider/cfn"
)

func ListProjectResources(state edge.StackState, region string) ([]provider.PhysicalResource, error) {
	var own recorded
	if err := state.Private.Into(&own); err != nil {
		return nil, err
	}
	var resources []provider.PhysicalResource
	for _, hostname := range slices.Sorted(maps.Keys(own.Hosts)) {
		host := own.Hosts[hostname]
		if host.Rule != "" {
			resources = append(resources, cfn.NewPhysicalResource("AWS::ElasticLoadBalancingV2::ListenerRule", host.Rule, region))
		}
		if host.Certificate == "" {
			continue
		}
		resources = append(resources, cfn.NewPhysicalResource("AWS::ElasticLoadBalancingV2::ListenerCertificate", host.Certificate, region))
		if host.CertificateRequested {
			resources = append(resources, cfn.NewPhysicalResource("AWS::CertificateManager::Certificate", host.Certificate, certs.RegionOfARN(host.Certificate)))
		}
	}
	return resources, nil
}
