package credential

import "github.com/ocelhq/ocel/platform/aws/provider/iam"

func Containers(s iam.Scope) []iam.Statement {
	return []iam.Statement{
		{
			Actions:   []string{"ecs:CreateCluster"},
			Resources: []string{s.ContainerClusterARN()},
			Condition: s.TaggedOnCreateForItsTier(),
		},
		{
			Actions: []string{
				"ecs:DeleteCluster",
				"ecs:DescribeClusters",
				"ecs:ListTagsForResource",
				"ecs:TagResource",
				"ecs:UntagResource",
			},
			Resources: []string{s.ContainerClusterARN()},
			Condition: s.TaggedForItsTier(),
		},
		{
			Actions:   []string{"iam:CreateServiceLinkedRole"},
			Resources: []string{iam.ECSLinkedRoleARN},
			Condition: iam.LinkedRoleFor("ecs.amazonaws.com"),
		},
		{
			Actions:   []string{"iam:CreateServiceLinkedRole"},
			Resources: []string{iam.ELBLinkedRoleARN},
			Condition: iam.LinkedRoleFor("elasticloadbalancing.amazonaws.com"),
		},
		{
			Actions:   []string{"iam:CreateServiceLinkedRole"},
			Resources: []string{iam.VPCOriginLinkedRoleARN},
			Condition: iam.LinkedRoleFor("vpcorigin.cloudfront.amazonaws.com"),
		},
		{
			Actions:   []string{"cloudfront:ListVpcOrigins"},
			Resources: []string{iam.AnyResource},
		},
		{
			Actions:   []string{"cloudfront:CreateVpcOrigin"},
			Resources: []string{iam.AnyResource},
			Condition: s.TaggedOnCreateForItsTier(),
		},
		{
			Actions:   []string{"cloudfront:TagResource"},
			Resources: []string{iam.ContainerVPCOriginARN},
			Condition: s.TaggedOnCreateForItsTier(),
		},
		{
			Actions: []string{
				"cloudfront:DeleteVpcOrigin",
				"cloudfront:GetVpcOrigin",
				"cloudfront:ListTagsForResource",
				"cloudfront:TagResource",
				"cloudfront:UntagResource",
				"cloudfront:UpdateVpcOrigin",
			},
			Resources: []string{iam.ContainerVPCOriginARN},
			Condition: s.TaggedForItsTier(),
		},
		{
			Actions:   []string{"acm:ImportCertificate"},
			Resources: []string{iam.AppCertificateARN},
			Condition: s.TaggedOnCreateForItsTier(),
		},
		{
			Actions:   []string{"acm:AddTagsToCertificate"},
			Resources: []string{iam.AppCertificateARN},
			Condition: iam.MergeConditions(s.TaggedOnCreateForItsTier(), iam.KeyPairSuppliedByCaller()),
		},
	}
}
