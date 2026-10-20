package envsourcesync

import "github.com/ocelhq/ocel/platform/aws/provider/iam"

func Credential(s iam.Scope) []iam.Statement {
	return []iam.Statement{
		{
			Actions:   []string{"lambda:PutFunctionEventInvokeConfig"},
			Resources: []string{s.EnvSourceSyncFunction, s.EnvSourceSyncFunction + ":$LATEST"},
		},
		{
			Actions:   []string{"scheduler:CreateScheduleGroup", "scheduler:TagResource"},
			Resources: []string{s.EnvSourceSyncScheduleGroup},
			Condition: s.TaggedOnCreateOnlyForItsTier(),
		},
		{
			Actions: []string{
				"scheduler:DeleteScheduleGroup",
				"scheduler:GetScheduleGroup",
				"scheduler:ListTagsForResource",
				"scheduler:UntagResource",
			},
			Resources: []string{s.EnvSourceSyncScheduleGroup},
			Condition: s.TaggedForItsTier(),
		},
		{
			Actions: []string{
				"scheduler:CreateSchedule",
				"scheduler:DeleteSchedule",
				"scheduler:GetSchedule",
				"scheduler:UpdateSchedule",
			},
			Resources: []string{s.EnvSourceSyncSchedule},
		},
	}
}
