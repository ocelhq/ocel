package task

import "github.com/ocelhq/ocel/platform/aws/provider/iam"

func credential(s iam.Scope) []iam.Statement {
	return []iam.Statement{
		{
			Actions:   []string{"scheduler:CreateScheduleGroup", "scheduler:TagResource"},
			Resources: []string{s.AppScheduleGroupARN()},
			Condition: s.TaggedOnCreateOnlyForItsTier(),
		},
		{
			Actions: []string{
				"scheduler:DeleteScheduleGroup",
				"scheduler:GetScheduleGroup",
				"scheduler:ListTagsForResource",
				"scheduler:UntagResource",
			},
			Resources: []string{s.AppScheduleGroupARN()},
			Condition: s.TaggedForItsTier(),
		},
		{
			Actions: []string{
				"scheduler:CreateSchedule",
				"scheduler:DeleteSchedule",
				"scheduler:GetSchedule",
				"scheduler:UpdateSchedule",
			},
			Resources: []string{s.AppScheduleARN()},
			Condition: s.TaggedOnlyAsItsTier(),
		},
	}
}
