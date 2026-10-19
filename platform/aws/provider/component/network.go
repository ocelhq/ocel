package component

import (
	"fmt"

	awsec2 "github.com/aws/aws-sdk-go-v2/service/ec2"

	"github.com/ocelhq/ocel/pkg/naming"
	ec2 "github.com/pulumi/pulumi-aws/sdk/v7/go/aws/ec2"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

const (
	MaxSecurityGroupDescriptionLen = 255
	MaxSubnetGroupDescriptionLen   = 255
)

type VPCSecurityGroup struct {
	Subject string
	Engine  string
	Port    int
	VPCID   string
	Clients string
	Egress  ec2.SecurityGroupEgressArray
	Tags    pulumi.StringMap
}

func NewVPCSecurityGroup(ctx *pulumi.Context, at naming.Coordinate, group VPCSecurityGroup) (*ec2.SecurityGroup, error) {
	if group.Clients == "" {
		return nil, fmt.Errorf("%s admits the tier's vpc-clients security group alone, and this deploy resolved none", group.Subject)
	}
	return ec2.NewSecurityGroup(ctx, naming.ResourceID(at.Kind, at.Name, "security-group"), &ec2.SecurityGroupArgs{
		Description: CapDescription(at.Description("security group for "+group.Subject), MaxSecurityGroupDescriptionLen),
		VpcId:       pulumi.String(group.VPCID),
		Ingress: ec2.SecurityGroupIngressArray{
			&ec2.SecurityGroupIngressArgs{
				Protocol:       pulumi.String("tcp"),
				FromPort:       pulumi.Int(group.Port),
				ToPort:         pulumi.Int(group.Port),
				SecurityGroups: pulumi.StringArray{pulumi.String(group.Clients)},
				Description:    CapDescription(at.Description(group.Engine+" access to "+group.Subject+" from the tier vpc-clients group"), MaxSecurityGroupDescriptionLen),
			},
		},
		Egress: group.Egress,
		Tags:   group.Tags,
	})
}

func LookupDefaultSubnets(ctx *pulumi.Context, vpcID string) ([]string, error) {
	subnets, err := ec2.GetSubnets(ctx, &ec2.GetSubnetsArgs{
		Filters: []ec2.GetSubnetsFilter{
			{Name: "vpc-id", Values: []string{vpcID}},
			{Name: "default-for-az", Values: []string{"true"}},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("look up default VPC subnets: %w", err)
	}
	return subnets.Ids, nil
}

type NetworkAPI interface {
	VPCsAPI
	awsec2.DescribeSubnetsAPIClient
}
