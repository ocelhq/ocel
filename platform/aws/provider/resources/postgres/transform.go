package postgres

import (
	"github.com/ocelhq/ocel/pkg/naming"
	ocelpulumi "github.com/ocelhq/ocel/pkg/provider/pulumi"
	"github.com/ocelhq/ocel/platform/aws/provider/component"
)

type Names struct {
	Cluster       string
	Instance      string
	Subnets       string
	SecurityGroup string
}

func NamesFor(at component.Stack, logicalName string) (Names, error) {
	cluster, err := ClusterName(at.Namespace, at.Tier(), component.ResourceCoordinate(at.Project, at.Env, logicalName, naming.KindDatabase))
	if err != nil {
		return Names{}, err
	}
	return Names{
		Cluster:       cluster,
		Instance:      cluster + naming.WordSeparator + instanceSuffix,
		Subnets:       cluster + naming.WordSeparator + subnetsSuffix,
		SecurityGroup: component.SecurityGroupName(cluster),
	}, nil
}

func (n Names) Surfaces() map[string][]ocelpulumi.ResourceRef {
	return map[string][]ocelpulumi.ResourceRef{
		"securityGroup": {n.securityGroup()},
		"subnetGroup":   {n.subnetGroup()},
		"cluster":       {n.cluster()},
		"instance":      {n.instance()},
	}
}

func (n Names) securityGroup() ocelpulumi.ResourceRef {
	return ocelpulumi.ResourceRef{Token: component.TokenEC2SecurityGroup, Name: n.SecurityGroup}
}

func (n Names) subnetGroup() ocelpulumi.ResourceRef {
	return ocelpulumi.ResourceRef{Token: component.TokenRDSSubnetGroup, Name: n.Subnets}
}

func (n Names) cluster() ocelpulumi.ResourceRef {
	return ocelpulumi.ResourceRef{Token: component.TokenRDSCluster, Name: n.Cluster}
}

func (n Names) instance() ocelpulumi.ResourceRef {
	return ocelpulumi.ResourceRef{Token: component.TokenRDSClusterMember, Name: n.Instance}
}
