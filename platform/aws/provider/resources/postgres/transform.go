package postgres

import (
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/platform/aws/provider/component"
)

func resourceNames(project, env, logicalName string) map[string]component.ResourceRef {
	at := component.ResourceCoordinate(project, env, logicalName, naming.KindDatabase)
	return map[string]component.ResourceRef{
		"securityGroup": {Token: component.TokenEC2SecurityGroup, Name: naming.ResourceID(at.Kind, at.Name, "security-group")},
		"subnetGroup":   {Token: component.TokenRDSSubnetGroup, Name: naming.ResourceID(at.Kind, at.Name, "subnet-group")},
		"cluster":       {Token: component.TokenRDSCluster, Name: naming.ResourceID(at.Kind, at.Name)},
		"instance":      {Token: component.TokenRDSClusterMember, Name: naming.ResourceID(at.Kind, at.Name, "instance")},
	}
}
