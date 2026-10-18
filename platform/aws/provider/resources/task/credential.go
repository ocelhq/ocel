package task

import "github.com/ocelhq/ocel/platform/aws/provider/iam"

func credential(iam.Scope) []iam.Statement {
	return []iam.Statement{
		{
			Actions: []string{
				"scheduler:CreateScheduleGroup",
				"scheduler:DeleteScheduleGroup",
				"scheduler:GetScheduleGroup",
				"scheduler:ListTagsForResource",
				"scheduler:TagResource",
				"scheduler:UntagResource",
			},
			Resources: []string{iam.AppScheduleGroupARN},
		},
		{
			Actions: []string{
				"scheduler:CreateSchedule",
				"scheduler:DeleteSchedule",
				"scheduler:GetSchedule",
				"scheduler:UpdateSchedule",
			},
			Resources: []string{iam.AppScheduleARN},
		},
	}
}
