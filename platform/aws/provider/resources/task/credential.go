package task

import "github.com/ocelhq/ocel/platform/aws/provider/iam"

func credential(s iam.Scope) []iam.Statement {
	return []iam.Statement{
		{
			Actions:   []string{"scheduler:CreateScheduleGroup", "scheduler:TagResource"},
			Resources: []string{iam.AppScheduleGroupARN},
			Condition: s.TaggedOnCreateOnlyForItsTier(),
		},
		{
			Actions: []string{
				"scheduler:DeleteScheduleGroup",
				"scheduler:GetScheduleGroup",
				"scheduler:ListTagsForResource",
				"scheduler:UntagResource",
			},
			Resources: []string{iam.AppScheduleGroupARN},
			Condition: s.TaggedByOcelForItsTier(),
		},
		{
			Actions: []string{
				"scheduler:CreateSchedule",
				"scheduler:DeleteSchedule",
				"scheduler:GetSchedule",
				"scheduler:UpdateSchedule",
			},
			Resources: []string{iam.AppScheduleARN},
			Condition: s.TaggedOnlyAsItsTier(),
		},
	}
}
