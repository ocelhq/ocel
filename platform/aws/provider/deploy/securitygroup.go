package deploy

import (
	ec2 "github.com/pulumi/pulumi-aws/sdk/v7/go/aws/ec2"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/naming"
)

type vpcSecurityGroup struct {
	Subject string
	Engine  string
	Port    int
	VPCID   string
	VPCCIDR string
	Egress  ec2.SecurityGroupEgressArray
	Tags    pulumi.StringMap
}

func newVPCSecurityGroup(ctx *pulumi.Context, at naming.Coordinate, group vpcSecurityGroup) (*ec2.SecurityGroup, error) {
	return ec2.NewSecurityGroup(ctx, naming.ResourceID(at.Kind, at.Name, "security-group"), &ec2.SecurityGroupArgs{
		Description: capDescription(at.Description("security group for "+group.Subject), maxSecurityGroupDescriptionLen),
		VpcId:       pulumi.String(group.VPCID),
		Ingress: ec2.SecurityGroupIngressArray{
			&ec2.SecurityGroupIngressArgs{
				Protocol:    pulumi.String("tcp"),
				FromPort:    pulumi.Int(group.Port),
				ToPort:      pulumi.Int(group.Port),
				CidrBlocks:  pulumi.StringArray{pulumi.String(group.VPCCIDR)},
				Description: capDescription(at.Description(group.Engine+" access to "+group.Subject+" from within the VPC"), maxSecurityGroupDescriptionLen),
			},
		},
		Egress: group.Egress,
		Tags:   group.Tags,
	})
}
