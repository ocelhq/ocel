package functionnetwork

import (
	"github.com/ocelhq/ocel/platform/aws/provider/component"
	"github.com/ocelhq/ocel/platform/aws/provider/iam"
)

func Credential(s iam.Scope) []iam.Statement {
	return []iam.Statement{
		{
			Actions: []string{
				"ec2:DescribeAddresses",
				"ec2:DescribeAddressesAttribute",
				"ec2:DescribeNatGateways",
			},
			Resources: []string{iam.AnyResource},
		},
		{
			Actions:   []string{"servicequotas:GetServiceQuota"},
			Resources: []string{component.ElasticIPQuota.ARN()},
		},
		{
			Actions:   []string{"ec2:AllocateAddress"},
			Resources: []string{iam.AppElasticIPARN},
			Condition: s.TaggedOnCreateForItsTier(),
		},
		{
			Actions:   []string{"ec2:CreateNatGateway"},
			Resources: []string{iam.AppNATGatewayARN, iam.AppElasticIPARN},
			Condition: s.TaggedOnCreateForItsTier(),
		},
		{
			Actions:   []string{"ec2:CreateNatGateway"},
			Resources: []string{iam.AppSubnetARN},
			Condition: s.TaggedForItsTier(),
		},
		{
			Actions:   []string{"ec2:CreateTags"},
			Resources: []string{iam.AppElasticIPARN, iam.AppNATGatewayARN},
			Condition: map[string]any{
				"StringEquals": map[string]any{"ec2:CreateAction": []string{"AllocateAddress", "CreateNatGateway"}},
			},
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
	}
}
