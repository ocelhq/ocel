package container

import (
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/platform/aws/provider/component"
)

const (
	maxServiceNameLen = 255

	targetGroupWord = "tg"
	rulePart        = "rule"
	scalingPart     = "scaling"
)

type Names struct {
	Service     string
	TargetGroup string
	Rule        string
	Role        component.Name
}

func NamesFor(namespace, project string, stack naming.StackName) (Names, error) {
	return namesAt(namespace, serviceCoordinate(project, stack))
}

func namesAt(namespace string, service naming.Coordinate) (Names, error) {
	physical, err := component.PhysicalName(namespace, service, maxServiceNameLen)
	if err != nil {
		return Names{}, err
	}
	roleAt := service
	roleAt.Kind, roleAt.Name = naming.KindRole, component.AppRoleWord
	role, err := component.PhysicalName(namespace, roleAt, component.MaxRoleNameLen)
	if err != nil {
		return Names{}, err
	}
	group := TargetGroupName(namespace, service)
	return Names{
		Service:     physical,
		TargetGroup: group,
		Rule:        component.NameOf(group).Part(rulePart),
		Role:        component.NameOf(role),
	}, nil
}

func (n Names) Surfaces() map[string][]component.ResourceRef {
	return map[string][]component.ResourceRef{
		"task":        {{Token: component.TokenECSTaskDefinition, Name: n.Service}},
		"service":     {{Token: component.TokenECSService, Name: n.Service}},
		"targetGroup": {{Token: component.TokenLBTargetGroup, Name: n.TargetGroup}},
		"rule":        {{Token: component.TokenLBListenerRule, Name: n.Rule}},
		"role":        {{Token: component.TokenIAMRole, Name: n.Role.Pulumi}},
	}
}

func TargetGroupName(namespace string, service naming.Coordinate) string {
	group := service
	group.Name = targetGroupWord + naming.FieldSeparator + service.Release.String()
	return naming.AppFence(namespace) + targetGroupWord + naming.WordSeparator + group.HashTag(namespace)
}
