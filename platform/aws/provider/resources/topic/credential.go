package topic

import "github.com/ocelhq/ocel/platform/aws/provider/iam"

func credential(s iam.Scope) []iam.Statement {
	return []iam.Statement{
		{
			Actions:   []string{"sqs:CreateQueue", "sqs:TagQueue"},
			Resources: []string{iam.AppQueueARN},
			Condition: s.TaggedOnCreateOnlyForItsTier(),
		},
		{
			Actions: []string{
				"sqs:DeleteQueue",
				"sqs:GetQueueAttributes",
				"sqs:GetQueueUrl",
				"sqs:ListQueueTags",
				"sqs:SetQueueAttributes",
				"sqs:UntagQueue",
			},
			Resources: []string{iam.AppQueueARN},
			Condition: s.TaggedByOcelForItsTier(),
		},
		{
			Actions:   []string{"sns:CreateTopic", "sns:TagResource"},
			Resources: []string{iam.AppTopicARN},
			Condition: s.TaggedOnCreateOnlyForItsTier(),
		},
		{
			Actions: []string{
				"sns:DeleteTopic",
				"sns:GetTopicAttributes",
				"sns:ListTagsForResource",
				"sns:SetTopicAttributes",
				"sns:Subscribe",
				"sns:UntagResource",
			},
			Resources: []string{iam.AppTopicARN},
			Condition: s.TaggedByOcelForItsTier(),
		},
		{
			Actions: []string{
				"sns:GetSubscriptionAttributes",
				"sns:SetSubscriptionAttributes",
				"sns:Unsubscribe",
			},
			Resources: []string{iam.AppSubscriptionARN},
			Condition: s.TaggedOnlyAsItsTier(),
		},
	}
}
