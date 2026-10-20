package function

import "github.com/ocelhq/ocel/platform/aws/provider/iam"

func Credential(s iam.Scope) []iam.Statement {
	return []iam.Statement{
		{
			Actions:   []string{"lambda:CreateFunction"},
			Resources: []string{iam.AppFunctionARN},
			Condition: s.TaggedOnCreateForItsTier(),
		},
		{
			Actions: []string{
				"lambda:AddPermission",
				"lambda:CreateFunctionUrlConfig",
				"lambda:DeleteFunction",
				"lambda:DeleteFunctionUrlConfig",
				"lambda:GetFunction",
				"lambda:GetFunctionConfiguration",
				"lambda:GetFunctionUrlConfig",
				"lambda:GetPolicy",
				"lambda:InvokeFunction",
				"lambda:ListTags",
				"lambda:PublishVersion",
				"lambda:RemovePermission",
				"lambda:UntagResource",
				"lambda:UpdateFunctionCode",
				"lambda:UpdateFunctionConfiguration",
				"lambda:UpdateFunctionUrlConfig",
			},
			Resources: []string{iam.AppFunctionARN},
			Condition: s.TaggedByOcelForItsTier(),
		},
		{
			Actions:   []string{"lambda:TagResource"},
			Resources: []string{iam.AppFunctionARN},
			Condition: s.RetaggedByOcelOnlyAsItsTier(),
		},
		{
			Actions:   []string{"iam:CreateRole"},
			Resources: []string{iam.AppRoleARN},
			Condition: iam.MergeConditions(s.TaggedOnCreateForItsTier(), s.WithinAppBoundary()),
		},
		{
			Actions: []string{
				"iam:DeleteRole",
				"iam:GetRole",
				"iam:GetRolePolicy",
				"iam:ListAttachedRolePolicies",
				"iam:ListInstanceProfilesForRole",
				"iam:ListRolePolicies",
				"iam:ListRoleTags",
				"iam:UntagRole",
				"iam:UpdateRole",
			},
			Resources: []string{iam.AppRoleARN},
			Condition: s.TaggedByOcelForItsTier(),
		},
		{
			Actions:   []string{"iam:TagRole"},
			Resources: []string{iam.AppRoleARN},
			Condition: s.RetaggedByOcelOnlyAsItsTier(),
		},
		{
			Actions: []string{
				"iam:DeleteRolePolicy",
				"iam:PutRolePermissionsBoundary",
				"iam:PutRolePolicy",
			},
			Resources: []string{iam.AppRoleARN},
			Condition: iam.MergeConditions(s.TaggedByOcelForItsTier(), s.WithinAppBoundary()),
		},
		{
			Actions:   []string{"iam:AttachRolePolicy", "iam:DetachRolePolicy"},
			Resources: []string{iam.AppRoleARN},
			Condition: iam.MergeConditions(s.AttachedPolicyIsAServiceRole(true), s.WithinAppBoundary(), s.MadeByItsStacks()),
		},
		{
			Actions:   []string{"iam:PassRole"},
			Resources: []string{s.AppRoles},
			Condition: iam.PassedTo(iam.LambdaPrincipal),
		},
		{
			Actions:   []string{"iam:PassRole"},
			Resources: []string{s.AppRoles},
			Condition: iam.PassedTo(iam.ECSTasksPrincipal),
		},
		{
			Actions:   []string{"iam:PassRole"},
			Resources: []string{s.AppRoles},
			Condition: iam.PassedTo(iam.SchedulerPrincipal),
		},
		{
			Actions:   []string{"logs:CreateLogGroup"},
			Resources: append(s.ContainerLogGroupARNs(), iam.FunctionLogGroupARN),
			Condition: s.TaggedOnCreate(),
		},
		{
			Actions: []string{
				"logs:DeleteLogGroup",
				"logs:FilterLogEvents",
				"logs:ListTagsForResource",
				"logs:PutRetentionPolicy",
				"logs:StartLiveTail",
				"logs:TagResource",
				"logs:UntagResource",
			},
			Resources: append(s.ContainerLogGroupARNs(), iam.FunctionLogGroupARN),
			Condition: s.TaggedByOcel(),
		},
		{
			Actions:   []string{"logs:DescribeLogGroups"},
			Resources: []string{iam.AnyResource},
		},
	}
}
