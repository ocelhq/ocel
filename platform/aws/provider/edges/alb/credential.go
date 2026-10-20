package alb

import (
	"slices"

	"github.com/ocelhq/ocel/platform/aws/provider/iam"
)

func Credential(s iam.Scope) []iam.Statement {
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
			Resources: slices.Concat(s.ContainerBalancerARNs(), []string{iam.AppTargetGroupARN, s.ContainerTrustStoreARN()}),
			Condition: s.TaggedOnCreateForItsTier(),
		},
		{
			Actions: []string{
				"elasticloadbalancing:AddListenerCertificates",
				"elasticloadbalancing:RemoveListenerCertificates",
			},
			Resources: s.ContainerListenerARNs(),
			Condition: s.TaggedByOcelForItsTier(),
		},
		{
			Actions:   []string{"elasticloadbalancing:AddTags", "elasticloadbalancing:ModifyTrustStore", "elasticloadbalancing:DeleteTrustStore"},
			Resources: []string{s.ContainerTrustStoreARN()},
			Condition: s.TaggedByOcelForItsTier(),
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
			Resources: slices.Concat(s.ContainerBalancerARNs(), s.ContainerListenerARNs(), s.ContainerRuleARNs(), []string{iam.AppTargetGroupARN}),
			Condition: s.TaggedByOcelForItsTier(),
		},
		{
			Actions:   []string{"elasticloadbalancing:AddTags"},
			Resources: slices.Concat(s.ContainerBalancerARNs(), s.ContainerListenerARNs(), s.ContainerRuleARNs(), []string{iam.AppTargetGroupARN, s.ContainerTrustStoreARN()}),
			Condition: s.TaggedOnCreateForItsTier(),
		},
	}
}
