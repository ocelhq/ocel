package component

import (
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
	Owner   string
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
	name := SecurityGroupName(group.Owner)
	tags := pulumi.StringMap{nameTag: pulumi.String(name)}
	maps.Copy(tags, group.Tags)
	return ec2.NewSecurityGroup(ctx, name, &ec2.SecurityGroupArgs{
		Name:        pulumi.String(name),
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
		Tags:   tags,
	})
}

func SecurityGroupName(owner string) string {
	return owner + naming.WordSeparator + SecurityGroupSuffix
}

func LookupDefaultSubnets(ctx *pulumi.Context, vpcID string) ([]string, error) {
	return LookupDefaultSubnetsIn(ctx, vpcID, nil)
}

func LookupDefaultSubnetsIn(ctx *pulumi.Context, vpcID string, zoneIDs []string) ([]string, error) {
	filters := []ec2.GetSubnetsFilter{
		{Name: "vpc-id", Values: []string{vpcID}},
		{Name: "default-for-az", Values: []string{"true"}},
	}
	if zoneIDs != nil {
		filters = append(filters, ec2.GetSubnetsFilter{Name: "availability-zone-id", Values: zoneIDs})
	}
	subnets, err := ec2.GetSubnets(ctx, &ec2.GetSubnetsArgs{Filters: filters})
	if err != nil {
		return nil, fmt.Errorf("look up default VPC subnets: %w", err)
	}
	return subnets.Ids, nil
}

type NetworkAPI interface {
	VPCsAPI
	awsec2.DescribeSubnetsAPIClient
}
