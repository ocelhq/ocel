package worker

import "github.com/ocelhq/ocel/platform/aws/provider/iam"

func Credential(s iam.Scope) []iam.Statement {
	return []iam.Statement{
		{
			Actions:   []string{"lambda:CreateEventSourceMapping"},
			Resources: []string{iam.AnyResource},
			Condition: iam.MergeConditions(s.TaggedOnCreateForItsTier(), s.OfAppWorker()),
		},
		{
			Actions: []string{
				"lambda:DeleteEventSourceMapping",
				"lambda:GetEventSourceMapping",
				"lambda:UpdateEventSourceMapping",
			},
			Resources: []string{iam.EventSourceMappingARN},
			Condition: iam.MergeConditions(s.TaggedByOcelForItsTier(), s.OfAppWorker()),
		},
		{
			Actions:   []string{"lambda:ListTags", "lambda:UntagResource"},
			Resources: []string{iam.EventSourceMappingARN},
			Condition: s.TaggedByOcelForItsTier(),
		},
		{
			Actions:   []string{"lambda:TagResource"},
			Resources: []string{iam.EventSourceMappingARN},
			Condition: s.RetaggedByOcelOnlyAsItsTier(),
		},
		{
			Actions:   []string{"cloudwatch:PutMetricAlarm", "cloudwatch:TagResource"},
			Resources: []string{s.AppAlarmARN()},
			Condition: s.TaggedOnlyAsItsTier(),
		},
		{
			Actions:   []string{"cloudwatch:DeleteAlarms", "cloudwatch:ListTagsForResource", "cloudwatch:UntagResource"},
			Resources: []string{s.AppAlarmARN()},
			Condition: s.TaggedByOcelForItsTier(),
		},
	}
}
