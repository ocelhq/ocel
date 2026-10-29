package connector

import (
	"github.com/ocelhq/ocel/platform/aws/provider/account"
	"github.com/ocelhq/ocel/platform/aws/provider/iam"
)

func Credential(s iam.Scope) []iam.Statement {
	return []iam.Statement{
		{
			Actions:   []string{"cloudformation:DescribeStacks"},
			Resources: []string{"arn:aws:cloudformation:*:*:stack/" + StackName(account.Namespace(s.Namespace)) + "/*"},
		},
	}
}
