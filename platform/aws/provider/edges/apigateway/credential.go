package apigateway

import "github.com/ocelhq/ocel/platform/aws/provider/iam"

func Credential(iam.Scope) []iam.Statement {
	return []iam.Statement{
		{
			Actions:   []string{"tag:GetResources"},
			Resources: []string{iam.AnyResource},
		},
	}
}
