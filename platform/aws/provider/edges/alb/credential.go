package alb

import "github.com/ocelhq/ocel/platform/aws/provider/iam"

func Credential(iam.Scope) []iam.Statement {
	return []iam.Statement{
		{
			Actions: []string{
				"elasticloadbalancing:DescribeListenerAttributes",
				"elasticloadbalancing:DescribeListeners",
				"elasticloadbalancing:DescribeLoadBalancerAttributes",
				"elasticloadbalancing:DescribeLoadBalancers",
				"elasticloadbalancing:DescribeRules",
				"elasticloadbalancing:DescribeTags",
				"elasticloadbalancing:DescribeTargetGroupAttributes",
				"elasticloadbalancing:DescribeTargetGroups",
				"elasticloadbalancing:DescribeTargetHealth",
				"elasticloadbalancing:DescribeListenerCertificates",
				"elasticloadbalancing:DescribeTrustStores",
			},
			Resources: []string{iam.AnyResource},
		},
		{
			Actions:   []string{"elasticloadbalancing:CreateLoadBalancer", "elasticloadbalancing:CreateTargetGroup", "elasticloadbalancing:CreateTrustStore"},
			Resources: []string{iam.ContainerBalancerARN, iam.AppTargetGroupARN, iam.ContainerTrustStoreARN},
			Condition: iam.TaggedOnCreate(),
		},
		{
			Actions: []string{
				"elasticloadbalancing:AddListenerCertificates",
				"elasticloadbalancing:RemoveListenerCertificates",
			},
			Resources: []string{iam.ContainerListenerARN},
			Condition: iam.TaggedByOcel(),
		},
		{
			Actions:   []string{"elasticloadbalancing:AddTags", "elasticloadbalancing:ModifyTrustStore", "elasticloadbalancing:DeleteTrustStore"},
			Resources: []string{iam.ContainerTrustStoreARN},
			Condition: iam.TaggedByOcel(),
		},
		{
			Actions: []string{
				"elasticloadbalancing:AddTags",
				"elasticloadbalancing:CreateListener",
				"elasticloadbalancing:CreateRule",
				"elasticloadbalancing:DeleteListener",
				"elasticloadbalancing:DeleteLoadBalancer",
				"elasticloadbalancing:DeleteRule",
				"elasticloadbalancing:DeleteTargetGroup",
				"elasticloadbalancing:ModifyListener",
				"elasticloadbalancing:ModifyListenerAttributes",
				"elasticloadbalancing:ModifyLoadBalancerAttributes",
				"elasticloadbalancing:ModifyRule",
				"elasticloadbalancing:ModifyTargetGroup",
				"elasticloadbalancing:ModifyTargetGroupAttributes",
				"elasticloadbalancing:RemoveTags",
				"elasticloadbalancing:SetSecurityGroups",
				"elasticloadbalancing:SetSubnets",
			},
			Resources: []string{iam.ContainerBalancerARN, iam.ContainerListenerARN, iam.ContainerRuleARN, iam.AppTargetGroupARN},
			Condition: iam.TaggedByOcel(),
		},
		{
			Actions:   []string{"elasticloadbalancing:AddTags"},
			Resources: []string{iam.ContainerBalancerARN, iam.ContainerListenerARN, iam.ContainerRuleARN, iam.AppTargetGroupARN, iam.ContainerTrustStoreARN},
			Condition: iam.TaggedOnCreate(),
		},
	}
}
