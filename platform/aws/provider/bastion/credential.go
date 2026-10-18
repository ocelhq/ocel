package bastion

import "github.com/ocelhq/ocel/platform/aws/provider/iam"

const (
	ClusterARN                = "arn:aws:ecs:*:*:cluster/" + Prefix + "-*"
	TaskARN                   = "arn:aws:ecs:*:*:task/" + Prefix + "-*/*"
	TaskDefinitionARN         = "arn:aws:ecs:*:*:task-definition/" + Prefix + "-*:*"
	PortForwardingDocumentARN = "arn:aws:ssm:*:*:document/" + PortForwardingDocument
)

func onItsCluster() map[string]any {
	return map[string]any{"ArnEquals": map[string]any{"ecs:cluster": ClusterARN}}
}

func Credential(s iam.Scope) []iam.Statement {
	return []iam.Statement{
		{
			Actions:   []string{"iam:PassRole"},
			Resources: []string{s.BastionRole},
			Condition: iam.PassedTo(iam.ECSTasksPrincipal),
		},
		{
			Actions:   []string{"ecs:RunTask"},
			Resources: []string{TaskDefinitionARN},
			Condition: onItsCluster(),
		},
		{
			Actions:   []string{"ecs:DescribeTasks", "ecs:ExecuteCommand", "ecs:StopTask"},
			Resources: []string{TaskARN},
		},
		{
			Actions:   []string{"ecs:DescribeClusters", "ecs:ExecuteCommand"},
			Resources: []string{ClusterARN},
		},
		{
			Actions:   []string{"ecs:ListTasks"},
			Resources: []string{iam.AnyResource},
			Condition: onItsCluster(),
		},
		{
			Actions:   []string{"ecs:DeleteTaskDefinitions"},
			Resources: []string{TaskDefinitionARN},
		},
		{
			Actions:   []string{"ssm:StartSession"},
			Resources: []string{TaskARN, PortForwardingDocumentARN},
		},
	}
}
