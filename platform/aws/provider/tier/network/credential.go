package network

import (
	"github.com/ocelhq/ocel/platform/aws/provider/component"
	"github.com/ocelhq/ocel/platform/aws/provider/iam"
)

func Credential(s iam.Scope) []iam.Statement {
	return []iam.Statement{
		{
			Actions: []string{
				"ec2:DescribeInternetGateways",
				"ec2:DescribeNetworkAcls",
				"ec2:DescribeVpcAttribute",
			},
			Resources: []string{iam.AnyResource},
		},
		{
			Actions:   []string{"servicequotas:GetServiceQuota"},
			Resources: []string{component.VPCQuota.ARN()},
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
			Actions:   []string{"ec2:CreateTags"},
			Resources: []string{iam.AppVPCARN, iam.AppInternetGatewayARN},
			Condition: map[string]any{
				"StringEquals": map[string]any{"ec2:CreateAction": []string{"CreateInternetGateway", "CreateVpc"}},
			},
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
