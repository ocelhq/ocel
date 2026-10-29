package apigateway

import "github.com/ocelhq/ocel/platform/aws/provider/iam"

func Credential(s iam.Scope) []iam.Statement {
	return []iam.Statement{
		{
			Actions:   []string{"tag:GetResources"},
			Resources: []string{iam.AnyResource},
		},
		{
			Actions:   []string{"apigateway:DELETE", "apigateway:PATCH"},
			Resources: []string{iam.RestAPIResourcesARN},
			Condition: s.TaggedForItsTier(),
		},
		{
			Actions:   []string{"apigateway:GET"},
			Resources: []string{iam.DomainNameARN},
		},
		{
			Actions:   []string{"apigateway:AddCertificateToDomain", "apigateway:POST"},
			Resources: []string{iam.DomainNamesARN},
			Condition: s.TaggedOnCreateForItsTier(),
		},
		{
			Actions: []string{
				"apigateway:AddCertificateToDomain",
				"apigateway:CreateRoutingRule",
				"apigateway:DELETE",
				"apigateway:DeleteRoutingRule",
				"apigateway:ListRoutingRules",
				"apigateway:PATCH",
				"apigateway:POST",
				"apigateway:RemoveCertificateFromDomain",
				"apigateway:UpdateRoutingRule",
			},
			Resources: []string{iam.DomainNameARN, iam.DomainRoutingRulesARN},
			Condition: s.TaggedForItsTier(),
		},
	}
}
