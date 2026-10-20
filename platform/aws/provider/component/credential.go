package component

import "github.com/ocelhq/ocel/platform/aws/provider/iam"

func NetworkCredential(s iam.Scope) []iam.Statement {
	return []iam.Statement{
		{
			Actions: []string{
				"ec2:DescribeManagedPrefixLists",
				"ec2:DescribeNetworkInterfaces",
				"ec2:DescribeSecurityGroupRules",
				"ec2:DescribeSecurityGroups",
				"ec2:DescribeSubnets",
				"ec2:DescribeVpcs",
			},
			Resources: []string{iam.AnyResource},
		},
		{
			Actions:   []string{"ec2:CreateSecurityGroup"},
			Resources: []string{iam.AppSecurityGroupARN, iam.AppVPCARN},
			Condition: s.TaggedOnCreate(),
		},
		{
			Actions:   []string{"ec2:CreateTags"},
			Resources: []string{iam.AppSecurityGroupARN},
			Condition: map[string]any{
				"StringEquals": map[string]any{"ec2:CreateAction": "CreateSecurityGroup"},
			},
		},
		{
			Actions: []string{
				"ec2:AuthorizeSecurityGroupEgress",
				"ec2:AuthorizeSecurityGroupIngress",
				"ec2:DeleteSecurityGroup",
				"ec2:ModifySecurityGroupRules",
				"ec2:RevokeSecurityGroupEgress",
				"ec2:RevokeSecurityGroupIngress",
			},
			Resources: []string{iam.AppSecurityGroupARN},
			Condition: s.TaggedByOcel(),
		},
	}
}
