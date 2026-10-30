package credential

import (
	"github.com/ocelhq/ocel/platform/aws/provider/iam"
	"github.com/ocelhq/ocel/platform/aws/provider/quotas"
)

func Network(s iam.Scope) []iam.Statement {
	return []iam.Statement{
		{
			Actions: []string{
				"ec2:DescribeAvailabilityZones",
				"ec2:DescribeInternetGateways",
				"ec2:DescribeNetworkAcls",
				"ec2:DescribePrefixLists",
				"ec2:DescribeRouteTables",
				"ec2:DescribeVpcAttribute",
				"ec2:DescribeVpcEndpoints",
			},
			Resources: []string{iam.AnyResource},
		},
		{
			Actions:   []string{"servicequotas:GetServiceQuota"},
			Resources: []string{quotas.VPC.ARN()},
		},
		{
			Actions:   []string{"servicequotas:GetAWSDefaultServiceQuota"},
			Resources: []string{iam.AnyResource},
		},
		{
			Actions:   []string{"ec2:CreateVpc"},
			Resources: []string{iam.AppVPCARN},
			Condition: s.TaggedOnCreateForItsTier(),
		},
		{
			Actions:   []string{"ec2:CreateInternetGateway"},
			Resources: []string{iam.AppInternetGatewayARN},
			Condition: s.TaggedOnCreateForItsTier(),
		},
		{
			Actions:   []string{"ec2:CreateSubnet"},
			Resources: []string{iam.AppSubnetARN},
			Condition: s.TaggedOnCreateForItsTier(),
		},
		{
			Actions:   []string{"ec2:CreateRouteTable"},
			Resources: []string{iam.AppRouteTableARN},
			Condition: s.TaggedOnCreateForItsTier(),
		},
		{
			Actions:   []string{"ec2:CreateVpcEndpoint"},
			Resources: []string{iam.AppVPCEndpointARN},
			Condition: s.TaggedOnCreateForItsTier(),
		},
		{
			Actions:   []string{"ec2:CreateSubnet", "ec2:CreateRouteTable", "ec2:CreateVpcEndpoint"},
			Resources: []string{iam.AppVPCARN},
			Condition: s.TaggedForItsTier(),
		},
		{
			Actions:   []string{"ec2:CreateVpcEndpoint"},
			Resources: []string{iam.AppRouteTableARN},
			Condition: s.TaggedForItsTier(),
		},
		{
			Actions:   []string{"ec2:CreateTags"},
			Resources: []string{iam.AppVPCARN, iam.AppInternetGatewayARN, iam.AppSubnetARN, iam.AppRouteTableARN, iam.AppVPCEndpointARN},
			Condition: map[string]any{
				"StringEquals": map[string]any{"ec2:CreateAction": []string{"CreateInternetGateway", "CreateRouteTable", "CreateSubnet", "CreateVpc", "CreateVpcEndpoint"}},
			},
		},
		{
			Actions:   []string{"ec2:AssociateRouteTable", "ec2:DisassociateRouteTable"},
			Resources: []string{iam.AppRouteTableARN, iam.AppSubnetARN},
			Condition: s.TaggedForItsTier(),
		},
		{
			Actions:   []string{"ec2:CreateRoute", "ec2:DeleteRoute", "ec2:DeleteRouteTable", "ec2:ReplaceRoute"},
			Resources: []string{iam.AppRouteTableARN},
			Condition: s.TaggedForItsTier(),
		},
		{
			Actions:   []string{"ec2:DeleteSubnet", "ec2:ModifySubnetAttribute"},
			Resources: []string{iam.AppSubnetARN},
			Condition: s.TaggedForItsTier(),
		},
		{
			Actions:   []string{"ec2:DeleteVpcEndpoints", "ec2:ModifyVpcEndpoint"},
			Resources: []string{iam.AppVPCEndpointARN, iam.AppRouteTableARN},
			Condition: s.TaggedForItsTier(),
		},
		{
			Actions:   []string{"ec2:DeleteVpc", "ec2:ModifyVpcAttribute"},
			Resources: []string{iam.AppVPCARN},
			Condition: s.TaggedForItsTier(),
		},
		{
			Actions:   []string{"ec2:AttachInternetGateway", "ec2:DetachInternetGateway"},
			Resources: []string{iam.AppInternetGatewayARN, iam.AppVPCARN},
			Condition: s.TaggedForItsTier(),
		},
		{
			Actions:   []string{"ec2:DeleteInternetGateway"},
			Resources: []string{iam.AppInternetGatewayARN},
			Condition: s.TaggedForItsTier(),
		},
	}
}
