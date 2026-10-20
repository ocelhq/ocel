package containers

import "github.com/ocelhq/ocel/platform/aws/provider/iam"

func Credential(s iam.Scope) []iam.Statement {
	return []iam.Statement{
		{
			Actions:   []string{"ecs:CreateCluster"},
			Resources: []string{s.ContainerClusterARN()},
			Condition: s.TaggedOnCreate(),
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
			Condition: s.TaggedByOcel(),
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
			Actions:   []string{"cloudfront:ListVpcOrigins", "ec2:DescribeAvailabilityZones"},
			Resources: []string{iam.AnyResource},
		},
		{
			Actions:   []string{"cloudfront:CreateVpcOrigin"},
			Resources: []string{iam.AnyResource},
			Condition: s.TaggedOnCreate(),
		},
		{
			Actions:   []string{"cloudfront:TagResource"},
			Resources: []string{iam.ContainerVPCOriginARN},
			Condition: s.TaggedOnCreate(),
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
			Condition: s.TaggedByOcel(),
		},
		{
			Actions:   []string{"acm:ImportCertificate"},
			Resources: []string{iam.AppCertificateARN},
			Condition: s.TaggedOnCreate(),
		},
		{
			Actions:   []string{"acm:AddTagsToCertificate"},
			Resources: []string{iam.AppCertificateARN},
			Condition: iam.MergeConditions(s.TaggedOnCreate(), iam.KeyPairSuppliedByCaller()),
		},
	}
}
