package envsourcesync

import "github.com/ocelhq/ocel/platform/aws/provider/iam"

func Credential(s iam.Scope) []iam.Statement {
	return []iam.Statement{
		{
			Actions:   []string{"lambda:PutFunctionEventInvokeConfig"},
			Resources: []string{s.EnvSourceSyncFunction, s.EnvSourceSyncFunction + ":$LATEST"},
		},
	}
}
