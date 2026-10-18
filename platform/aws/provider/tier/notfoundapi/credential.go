package notfoundapi

import "github.com/ocelhq/ocel/platform/aws/provider/iam"

func Credential(s iam.Scope) []iam.Statement {
	return []iam.Statement{
		{
			Actions:   []string{"apigateway:POST"},
			Resources: []string{iam.RestAPIsARN},
			Condition: s.TaggedOnCreateForItsTier(),
		},
		{
			Actions:   []string{"apigateway:PUT"},
			Resources: []string{iam.APIGatewayTagsARN},
			Condition: iam.MergeConditions(s.TaggedOnCreateForItsTier(), s.TaggedOnlyAsItsTier()),
		},
		{
			Actions:   []string{"apigateway:GET", "apigateway:POST", "apigateway:PUT"},
			Resources: []string{iam.RestAPIResourcesARN},
			Condition: s.TaggedByOcelForItsTier(),
		},
	}
}

func ReclaimCredential(s iam.Scope) []iam.Statement {
	return []iam.Statement{
		{
			Actions:   []string{"apigateway:DELETE"},
			Resources: []string{iam.RestAPIResourcesARN},
			Condition: s.TaggedByOcelForItsTier(),
		},
	}
}
