package containerservice

import "github.com/ocelhq/ocel/pkg/naming"

const (
	MaxNameLen = 255

	MinInstancesTag = "ocel:min-instances"
	MaxInstancesTag = "ocel:max-instances"

	localName = "container"
)

func Coordinate(project string, stack naming.StackName) naming.Coordinate {
	return naming.Coordinate{
		Project: project,
		Env:     stack.Env,
		App:     stack.App,
		Kind:    naming.KindService,
		Name:    localName,
		Release: stack.Release,
	}
}

func NameAt(namespace string, at naming.Coordinate) (string, error) {
	return at.TaggedResourceName(namespace).Fit(MaxNameLen)
}
