package container

import (
	"github.com/ocelhq/ocel/pkg/naming"
	ocelpulumi "github.com/ocelhq/ocel/pkg/provider/pulumi"
	"github.com/ocelhq/ocel/platform/aws/provider/component"
	"github.com/ocelhq/ocel/platform/aws/provider/compute/containerservice"
)

const (
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
	return namesAt(namespace, containerservice.Coordinate(project, stack), component.AppRoleCoordinate(project, stack))
}

func namesAt(namespace string, service, roleAt naming.Coordinate) (Names, error) {
	physical, err := containerservice.NameAt(namespace, service)
	if err != nil {
		return Names{}, err
	}
	role, err := component.RoleName(namespace, roleAt)
	if err != nil {
		return Names{}, err
	}
	group := TargetGroupName(namespace, service)
	return Names{
		Service:     physical,
		TargetGroup: group,
		Rule:        component.NameOf(group).Part(rulePart),
		Role:        role,
	}, nil
}

func (n Names) Surfaces() map[string][]ocelpulumi.ResourceRef {
	return map[string][]ocelpulumi.ResourceRef{
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
