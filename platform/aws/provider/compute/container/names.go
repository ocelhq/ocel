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

	bindingPolicyWord   = "binding"
	tasksPolicyName     = "tasks"
	variablesPolicyName = "variables-read"
)

type containerNames struct {
	Service     string
	TargetGroup string
	Role        component.Name
}

func newContainerNames(namespace string, service naming.Coordinate) (containerNames, error) {
	physical, err := component.PhysicalName(namespace, service, maxServiceNameLen)
	if err != nil {
		return containerNames{}, err
	}
	roleAt := service
	roleAt.Kind, roleAt.Name = naming.KindRole, component.AppRoleWord
	role, err := component.PhysicalName(namespace, roleAt, component.MaxRoleNameLen)
	if err != nil {
		return containerNames{}, err
	}
	return containerNames{
		Service:     physical,
		TargetGroup: TargetGroupName(namespace, service),
		Role:        component.NameOf(role),
	}, nil
}

func TargetGroupName(namespace string, service naming.Coordinate) string {
	tag := naming.HashTag(namespace, service.Project, service.Env, service.App, string(service.Kind), targetGroupWord, service.Release.String())
	return naming.AppFence(namespace) + targetGroupWord + naming.WordSeparator + tag
}

func bindingPolicyName(binding string) string {
	return naming.Join(naming.WordSeparator, bindingPolicyWord, binding)
}
