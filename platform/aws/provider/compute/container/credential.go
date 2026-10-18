package container

import "github.com/ocelhq/ocel/platform/aws/provider/iam"

func Credential(iam.Scope) []iam.Statement {
	return []iam.Statement{
		{
			Actions:   []string{"ecs:RegisterTaskDefinition"},
			Resources: []string{iam.AppTaskDefinitionARN},
			Condition: iam.TaggedOnCreate(),
		},
		{
			Actions:   []string{"ecs:DeregisterTaskDefinition", "ecs:DescribeTaskDefinition", "ecs:ListTaskDefinitions"},
			Resources: []string{iam.AnyResource},
		},
		{
			Actions:   []string{"ecs:CreateService"},
			Resources: []string{iam.ContainerServiceARN},
			Condition: iam.TaggedOnCreate(),
		},
		{
			Actions: []string{
				"ecs:DeleteService",
				"ecs:DescribeServices",
				"ecs:ListTagsForResource",
				"ecs:TagResource",
				"ecs:UntagResource",
				"ecs:UpdateService",
			},
			Resources: []string{iam.ContainerServiceARN},
			Condition: iam.TaggedByOcel(),
		},
		{
			Actions:   []string{"application-autoscaling:RegisterScalableTarget"},
			Resources: []string{iam.ScalableTargetARN},
			Condition: iam.ScalesECSServices(iam.TaggedOnCreate()),
		},
		{
			Actions: []string{
				"application-autoscaling:DeleteScalingPolicy",
				"application-autoscaling:DeregisterScalableTarget",
				"application-autoscaling:PutScalingPolicy",
				"application-autoscaling:RegisterScalableTarget",
			},
			Resources: []string{iam.ScalableTargetARN},
			Condition: iam.ScalesECSServices(iam.TaggedByOcel()),
		},
		{
			Actions:   []string{"application-autoscaling:TagResource"},
			Resources: []string{iam.ScalableTargetARN},
			Condition: iam.TaggedOnCreate(),
		},
		{
			Actions:   []string{"application-autoscaling:TagResource", "application-autoscaling:UntagResource"},
			Resources: []string{iam.ScalableTargetARN},
			Condition: iam.TaggedByOcel(),
		},
		{
			Actions: []string{
				"application-autoscaling:DescribeScalableTargets",
				"application-autoscaling:DescribeScalingActivities",
				"application-autoscaling:DescribeScalingPolicies",
				"application-autoscaling:ListTagsForResource",
				"cloudwatch:DescribeAlarms",
			},
			Resources: []string{iam.AnyResource},
		},
		{
			Actions:   []string{"cloudwatch:DeleteAlarms", "cloudwatch:PutMetricAlarm"},
			Resources: []string{iam.ScalingAlarmARN},
		},
		{
			Actions:   []string{"iam:CreateServiceLinkedRole"},
			Resources: []string{iam.ECSScalingLinkedRoleARN},
			Condition: iam.LinkedRoleFor("ecs.application-autoscaling.amazonaws.com"),
		},
	}
}
