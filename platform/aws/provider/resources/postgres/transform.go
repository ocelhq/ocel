package postgres

import (
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/platform/aws/provider/component"
)

func resourceNames(at component.Stack, logicalName string) (map[string][]component.ResourceRef, error) {
	cluster, err := ClusterName(at.Namespace, at.Tier(), component.ResourceCoordinate(at.Project, at.Env, logicalName, naming.KindDatabase))
	if err != nil {
		return nil, err
	}
	return map[string][]component.ResourceRef{
		"securityGroup": {{Token: component.TokenEC2SecurityGroup, Name: component.SecurityGroupName(cluster)}},
		"subnetGroup":   {{Token: component.TokenRDSSubnetGroup, Name: cluster + naming.WordSeparator + subnetsSuffix}},
		"cluster":       {{Token: component.TokenRDSCluster, Name: cluster}},
		"instance":      {{Token: component.TokenRDSClusterMember, Name: cluster + naming.WordSeparator + instanceSuffix}},
	}, nil
}
