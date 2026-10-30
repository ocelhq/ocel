package credential

import "github.com/ocelhq/ocel/platform/aws/provider/iam"

func Realtime(s iam.Scope) []iam.Statement {
	return []iam.Statement{
		{
			Actions:   []string{"secretsmanager:CreateSecret", "secretsmanager:TagResource"},
			Resources: []string{s.SigningKeys},
			Condition: s.TaggedOnCreateOnlyForItsTier(),
		},
		{
			Actions:   []string{"secretsmanager:DeleteSecret", "secretsmanager:GetSecretValue"},
			Resources: []string{s.SigningKeys},
			Condition: s.TaggedForItsTier(),
		},
		{
			Actions:   []string{"secretsmanager:ListSecrets"},
			Resources: []string{iam.AnyResource},
		},
		{
			Actions:   []string{"appsync:CreateApi"},
			Resources: []string{iam.AnyResource},
			Condition: s.TaggedOnCreateForItsTier(),
		},
		{
			Actions:   []string{"appsync:CreateChannelNamespace"},
			Resources: []string{iam.AppChannelNamespaceARN},
			Condition: s.TaggedOnCreateOnlyForItsTier(),
		},
		{
			Actions:   []string{"appsync:TagResource"},
			Resources: []string{iam.AppEventAPIARN, iam.AppChannelNamespaceARN},
			Condition: s.RetaggedOnlyWithinItsTier(),
		},
		{
			Actions: []string{
				"appsync:DeleteApi",
				"appsync:DeleteChannelNamespace",
				"appsync:GetApi",
				"appsync:GetChannelNamespace",
				"appsync:ListTagsForResource",
				"appsync:UntagResource",
				"appsync:UpdateApi",
				"appsync:UpdateChannelNamespace",
			},
			Resources: []string{iam.AppEventAPIARN, iam.AppChannelNamespaceARN},
			Condition: s.TaggedForItsTier(),
		},
	}
}
