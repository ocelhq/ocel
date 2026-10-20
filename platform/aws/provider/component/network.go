package component

import (
	"context"
	"fmt"
	"maps"

	awsec2 "github.com/aws/aws-sdk-go-v2/service/ec2"

	"github.com/ocelhq/ocel/pkg/naming"
	ec2 "github.com/pulumi/pulumi-aws/sdk/v7/go/aws/ec2"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

const (
	MaxSecurityGroupDescriptionLen = 255
	MaxSubnetGroupDescriptionLen   = 255

	SecurityGroupSuffix = "sg"
	nameTag             = "Name"
)

type VPCSecurityGroup struct {
	Name    string
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
		return nil, fmt.Errorf("%s admits the clients security group of the tier's network alone, and this deploy resolved none", group.Subject)
	}
	tags := pulumi.StringMap{nameTag: pulumi.String(group.Name)}
	maps.Copy(tags, group.Tags)
	return ec2.NewSecurityGroup(ctx, group.Name, &ec2.SecurityGroupArgs{
		Name:        pulumi.String(group.Name),
		Description: CapDescription(at.Description("security group for "+group.Subject), MaxSecurityGroupDescriptionLen),
		VpcId:       pulumi.String(group.VPCID),
		Ingress: ec2.SecurityGroupIngressArray{
			&ec2.SecurityGroupIngressArgs{
				Protocol:       pulumi.String("tcp"),
				FromPort:       pulumi.Int(group.Port),
				ToPort:         pulumi.Int(group.Port),
				SecurityGroups: pulumi.StringArray{pulumi.String(group.Clients)},
				Description:    CapDescription(at.Description(group.Engine+" access to "+group.Subject+" from the tier clients group"), MaxSecurityGroupDescriptionLen),
			},
		},
		Egress: group.Egress,
		Tags:   tags,
	})
}

func SecurityGroupName(owner string) string {
	return owner + naming.WordSeparator + SecurityGroupSuffix
}

type NetworkAPI interface {
	awsec2.DescribeVpcsAPIClient
	DescribeAvailabilityZones(ctx context.Context, in *awsec2.DescribeAvailabilityZonesInput, optFns ...func(*awsec2.Options)) (*awsec2.DescribeAvailabilityZonesOutput, error)
	DescribeAddresses(ctx context.Context, in *awsec2.DescribeAddressesInput, optFns ...func(*awsec2.Options)) (*awsec2.DescribeAddressesOutput, error)
}
