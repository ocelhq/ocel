package cloudfront

import "github.com/ocelhq/ocel/platform/aws/provider/iam"

func Credential(s iam.Scope) []iam.Statement {
	return []iam.Statement{
		{
			Actions: []string{
				"cloudfront:CreateFunction",
				"cloudfront:DescribeFunction",
				"cloudfront:PublishFunction",
				"cloudfront:UpdateFunction",
			},
			Resources: s.EdgeFunctions,
		},
		{
			Actions:   []string{"cloudfront:CreateKeyValueStore", "cloudfront:DescribeKeyValueStore"},
			Resources: []string{iam.EdgeRoutesStoreARN},
		},
		{
			Actions:   []string{"cloudfront:CreateCachePolicy", "cloudfront:GetCachePolicy", "cloudfront:UpdateCachePolicy"},
			Resources: []string{iam.EdgeCachePolicyARN},
		},
		{
			Actions:   []string{"cloudfront:CreateResponseHeadersPolicy", "cloudfront:GetResponseHeadersPolicy", "cloudfront:UpdateResponseHeadersPolicy"},
			Resources: []string{iam.EdgeHeadersPolicyARN},
		},
		{
			Actions:   []string{"cloudfront:CreateOriginAccessControl", "cloudfront:GetOriginAccessControl", "cloudfront:UpdateOriginAccessControl"},
			Resources: []string{iam.EdgeAssetAccessARN},
		},
		{
			Actions:   []string{"cloudfront:ListCachePolicies", "cloudfront:ListOriginAccessControls", "cloudfront:ListResponseHeadersPolicies"},
			Resources: []string{iam.AnyResource},
		},
	}
}

func ReclaimCredential(s iam.Scope) []iam.Statement {
	return []iam.Statement{
		{
			Actions:   []string{"cloudfront:DeleteFunction"},
			Resources: s.EdgeFunctions,
		},
		{
			Actions:   []string{"cloudfront:DeleteKeyValueStore"},
			Resources: []string{iam.EdgeRoutesStoreARN},
		},
		{
			Actions:   []string{"cloudfront:DeleteCachePolicy"},
			Resources: []string{iam.EdgeCachePolicyARN},
		},
		{
			Actions:   []string{"cloudfront:DeleteResponseHeadersPolicy"},
			Resources: []string{iam.EdgeHeadersPolicyARN},
		},
		{
			Actions:   []string{"cloudfront:DeleteOriginAccessControl"},
			Resources: []string{iam.EdgeAssetAccessARN},
		},
	}
}
