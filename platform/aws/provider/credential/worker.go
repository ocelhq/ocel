package credential

import "github.com/ocelhq/ocel/platform/aws/provider/iam"

func Worker(s iam.Scope) []iam.Statement {
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
			Condition: iam.MergeConditions(s.TaggedForItsTier(), s.OfAppWorker()),
		},
		{
			Actions:   []string{"lambda:ListTags", "lambda:UntagResource"},
			Resources: []string{iam.EventSourceMappingARN},
			Condition: s.TaggedForItsTier(),
		},
		{
			Actions:   []string{"lambda:TagResource"},
			Resources: []string{iam.EventSourceMappingARN},
			Condition: s.RetaggedOnlyWithinItsTier(),
		},
		{
			Actions:   []string{"cloudwatch:PutMetricAlarm", "cloudwatch:TagResource"},
			Resources: []string{s.AppAlarmARN()},
			Condition: s.TaggedOnlyAsItsTier(),
		},
		{
			Actions:   []string{"cloudwatch:DeleteAlarms", "cloudwatch:ListTagsForResource", "cloudwatch:UntagResource"},
			Resources: []string{s.AppAlarmARN()},
			Condition: s.TaggedForItsTier(),
		},
	}
}
