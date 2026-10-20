package bastion

import (
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/platform/aws/provider/iam"
)

const PortForwardingDocumentARN = "arn:aws:ssm:*:*:document/" + PortForwardingDocument

func ClusterARN(namespace string, tier environment.Tier) string {
	return "arn:aws:ecs:*:*:cluster/" + NameFor(namespace, tier)
}

func TaskARN(namespace string, tier environment.Tier) string {
	return "arn:aws:ecs:*:*:task/" + NameFor(namespace, tier) + "/*"
}

func TaskDefinitionARN(namespace string, tier environment.Tier) string {
	return "arn:aws:ecs:*:*:task-definition/" + NameFor(namespace, tier) + ":*"
}

func onItsCluster(s iam.Scope) map[string]any {
	return map[string]any{"ArnEquals": map[string]any{"ecs:cluster": ClusterARN(s.Namespace, s.Tier)}}
}

func Credential(s iam.Scope) []iam.Statement {
	cluster, tasks, definitions := ClusterARN(s.Namespace, s.Tier), TaskARN(s.Namespace, s.Tier), TaskDefinitionARN(s.Namespace, s.Tier)
	return []iam.Statement{
		{
			Actions:   []string{"iam:PassRole"},
			Resources: []string{s.BastionRole},
			Condition: iam.PassedTo(iam.ECSTasksPrincipal),
		},
		{
			Actions:   []string{"ecs:RunTask"},
			Resources: []string{definitions},
			Condition: onItsCluster(s),
		},
		{
			Actions:   []string{"ecs:DescribeTasks", "ecs:ExecuteCommand", "ecs:StopTask"},
			Resources: []string{tasks},
		},
		{
			Actions:   []string{"ecs:DescribeClusters", "ecs:ExecuteCommand"},
			Resources: []string{cluster},
		},
		{
			Actions:   []string{"ecs:ListTasks"},
			Resources: []string{iam.AnyResource},
			Condition: onItsCluster(s),
		},
		{
			Actions:   []string{"ecs:DeleteTaskDefinitions"},
			Resources: []string{definitions},
		},
		{
			Actions:   []string{"ssm:StartSession"},
			Resources: []string{tasks, PortForwardingDocumentARN},
		},
	}
}
