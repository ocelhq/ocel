package cloudfront

import "github.com/ocelhq/ocel/platform/aws/provider/iam"

func Credential(s iam.Scope) []iam.Statement {
	return []iam.Statement{
		{
			Actions:   []string{"cloudfront:CreateDistribution"},
			Resources: []string{iam.AnyResource},
			Condition: s.TaggedOnCreateForItsTier(),
		},
		{
			Actions:   []string{"cloudfront:TagResource"},
			Resources: []string{iam.EdgeDistributionARN},
			Condition: iam.MergeConditions(s.TaggedOnCreateForItsTier(), s.TaggedOnlyAsItsTier()),
		},
		{
			Actions: []string{
				"cloudfront:DeleteDistribution",
				"cloudfront:GetDistribution",
				"cloudfront:GetDistributionConfig",
				"cloudfront:UpdateDistribution",
			},
			Resources: []string{iam.EdgeDistributionARN},
			Condition: s.TaggedByOcelForItsTier(),
		},
		{
			Actions:   []string{"cloudfront:ListDistributions"},
			Resources: []string{iam.AnyResource},
		},
	}
}
