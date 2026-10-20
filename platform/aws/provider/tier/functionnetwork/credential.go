package functionnetwork

import "github.com/ocelhq/ocel/platform/aws/provider/iam"

func Credential(s iam.Scope) []iam.Statement {
	return []iam.Statement{
		{
			Actions: []string{
				"ec2:DescribeAddresses",
				"ec2:DescribeAddressesAttribute",
				"ec2:DescribeNatGateways",
				"ec2:DescribePrefixLists",
				"ec2:DescribeRouteTables",
				"ec2:DescribeVpcEndpoints",
			},
			Resources: []string{iam.AnyResource},
		},
		{
			Actions:   []string{"ec2:CreateSubnet"},
			Resources: []string{iam.AppSubnetARN, iam.AppVPCARN},
			Condition: s.TaggedOnCreateForItsTier(),
		},
		{
			Actions:   []string{"ec2:AllocateAddress"},
			Resources: []string{iam.AppElasticIPARN},
			Condition: s.TaggedOnCreateForItsTier(),
		},
		{
			Actions:   []string{"ec2:CreateNatGateway"},
			Resources: []string{iam.AppNATGatewayARN, iam.AppSubnetARN, iam.AppElasticIPARN},
			Condition: s.TaggedOnCreateForItsTier(),
		},
		{
			Actions:   []string{"ec2:CreateRouteTable"},
			Resources: []string{iam.AppRouteTableARN, iam.AppVPCARN},
			Condition: s.TaggedOnCreateForItsTier(),
		},
		{
			Actions:   []string{"ec2:CreateVpcEndpoint"},
			Resources: []string{iam.AppVPCEndpointARN, iam.AppVPCARN, iam.AppRouteTableARN},
			Condition: s.TaggedOnCreateForItsTier(),
		},
		{
			Actions:   []string{"ec2:CreateTags"},
			Resources: []string{iam.AppSubnetARN, iam.AppElasticIPARN, iam.AppNATGatewayARN, iam.AppRouteTableARN, iam.AppVPCEndpointARN},
			Condition: map[string]any{
				"StringEquals": map[string]any{"ec2:CreateAction": []string{
					"AllocateAddress", "CreateNatGateway", "CreateRouteTable", "CreateSubnet", "CreateVpcEndpoint",
				}},
			},
		},
		{
			Actions: []string{
				"ec2:AssociateRouteTable",
				"ec2:DisassociateRouteTable",
			},
			Resources: []string{iam.AppRouteTableARN, iam.AppSubnetARN},
			Condition: s.TaggedForItsTier(),
		},
		{
			Actions: []string{
				"ec2:CreateRoute",
				"ec2:DeleteRoute",
				"ec2:DeleteRouteTable",
				"ec2:ReplaceRoute",
			},
			Resources: []string{iam.AppRouteTableARN},
			Condition: s.TaggedForItsTier(),
		},
		{
			Actions:   []string{"ec2:DeleteSubnet", "ec2:ModifySubnetAttribute"},
			Resources: []string{iam.AppSubnetARN},
			Condition: s.TaggedForItsTier(),
		},
		{
			Actions:   []string{"ec2:DisassociateAddress", "ec2:ReleaseAddress"},
			Resources: []string{iam.AppElasticIPARN},
			Condition: s.TaggedForItsTier(),
		},
		{
			Actions:   []string{"ec2:DeleteNatGateway"},
			Resources: []string{iam.AppNATGatewayARN},
			Condition: s.TaggedForItsTier(),
		},
		{
			Actions:   []string{"ec2:DeleteVpcEndpoints", "ec2:ModifyVpcEndpoint"},
			Resources: []string{iam.AppVPCEndpointARN, iam.AppRouteTableARN},
			Condition: s.TaggedForItsTier(),
		},
	}
}
