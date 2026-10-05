package images

import (
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
)

const LocalNamespace = "ocel"

const projectSeparator = "."

func RegistryRepository(project, repository string) string {
	return naming.Sanitize(project) + projectSeparator + naming.RepositorySegment(repository)
}

func Ref(project, repository, tag string, target provider.RegistryTarget) string {
	if !target.Named() {
		return LocalNamespace + naming.PathSeparator + naming.Sanitize(project) + naming.PathSeparator + naming.RepositorySegment(repository) + ":" + tag
	}
	return target.ImageRef(RegistryRepository(project, repository), tag)
}
