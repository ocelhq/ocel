package topic

import "github.com/ocelhq/ocel/platform/aws/provider/iam"

func credential(iam.Scope) []iam.Statement {
	return []iam.Statement{
		{
			Actions: []string{
				"sqs:CreateQueue",
				"sqs:DeleteQueue",
				"sqs:GetQueueAttributes",
				"sqs:GetQueueUrl",
				"sqs:ListQueueTags",
				"sqs:SetQueueAttributes",
				"sqs:TagQueue",
				"sqs:UntagQueue",
			},
			Resources: []string{iam.AppQueueARN},
		},
		{
			Actions: []string{
				"sns:CreateTopic",
				"sns:DeleteTopic",
				"sns:GetSubscriptionAttributes",
				"sns:GetTopicAttributes",
				"sns:ListTagsForResource",
				"sns:SetSubscriptionAttributes",
				"sns:SetTopicAttributes",
				"sns:Subscribe",
				"sns:TagResource",
				"sns:Unsubscribe",
				"sns:UntagResource",
			},
			Resources: []string{iam.AppTopicARN, iam.AppSubscriptionARN},
		},
	}
}
