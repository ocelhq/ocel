package realtime

import "github.com/ocelhq/ocel/platform/aws/provider/iam"

func credential(s iam.Scope) []iam.Statement {
	return []iam.Statement{
		{
			Actions:   []string{"secretsmanager:CreateSecret", "secretsmanager:TagResource"},
			Resources: []string{s.SigningKeys},
			Condition: iam.TaggedOnCreate(),
		},
		{
			Actions:   []string{"secretsmanager:DeleteSecret", "secretsmanager:GetSecretValue"},
			Resources: []string{s.SigningKeys},
			Condition: iam.TaggedByOcel(),
		},
		{
			Actions:   []string{"secretsmanager:ListSecrets"},
			Resources: []string{iam.AnyResource},
		},
		{
			Actions:   []string{"appsync:CreateApi"},
			Resources: []string{iam.AnyResource},
			Condition: iam.TaggedOnCreate(),
		},
		{
			Actions:   []string{"appsync:CreateChannelNamespace"},
			Resources: []string{iam.AppChannelNamespaceARN},
			Condition: iam.TaggedOnCreate(),
		},
		{
			Actions: []string{
				"appsync:DeleteApi",
				"appsync:DeleteChannelNamespace",
				"appsync:GetApi",
				"appsync:GetChannelNamespace",
				"appsync:ListTagsForResource",
				"appsync:TagResource",
				"appsync:UntagResource",
				"appsync:UpdateApi",
				"appsync:UpdateChannelNamespace",
			},
			Resources: []string{iam.AppEventAPIARN, iam.AppChannelNamespaceARN},
			Condition: iam.TaggedByOcel(),
		},
	}
}
