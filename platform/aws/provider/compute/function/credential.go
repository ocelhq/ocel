package function

import "github.com/ocelhq/ocel/platform/aws/provider/iam"

func Credential(s iam.Scope) []iam.Statement {
	return []iam.Statement{
		{
			Actions:   []string{"lambda:CreateFunction"},
			Resources: s.FunctionARNs(),
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
			Resources: s.FunctionARNs(),
			Condition: s.TaggedByOcelForItsTier(),
		},
		{
			Actions:   []string{"lambda:TagResource"},
			Resources: s.FunctionARNs(),
			Condition: s.RetaggedByOcelOnlyAsItsTier(),
		},
		{
			Actions:   []string{"iam:CreateRole"},
			Resources: s.AppBoundaryRoles(),
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
			Resources: s.AppBoundaryRoles(),
			Condition: s.TaggedByOcelForItsTier(),
		},
		{
			Actions:   []string{"iam:TagRole"},
			Resources: s.AppBoundaryRoles(),
			Condition: s.RetaggedByOcelOnlyAsItsTier(),
		},
		{
			Actions: []string{
				"iam:DeleteRolePolicy",
				"iam:PutRolePermissionsBoundary",
				"iam:PutRolePolicy",
			},
			Resources: s.AppBoundaryRoles(),
			Condition: iam.MergeConditions(s.TaggedByOcelForItsTier(), s.WithinAppBoundary()),
		},
		{
			Actions:   []string{"iam:AttachRolePolicy", "iam:DetachRolePolicy"},
			Resources: s.AppBoundaryRoles(),
			Condition: iam.MergeConditions(iam.AttachedPolicyIsAServiceRole(), s.WithinAppBoundary(), s.TaggedByOcelForItsTier()),
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
			Resources: append(s.ContainerLogGroupARNs(), s.FunctionLogGroupARNs()...),
			Condition: s.TaggedOnCreateForItsTier(),
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
			Resources: append(s.ContainerLogGroupARNs(), s.FunctionLogGroupARNs()...),
			Condition: s.TaggedByOcelForItsTier(),
		},
		{
			Actions:   []string{"logs:DescribeLogGroups"},
			Resources: []string{iam.AnyResource},
		},
	}
}
