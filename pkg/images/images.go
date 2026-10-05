package images

import (
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
)

const localNamespace = "ocel"

const projectSeparator = "."

func LocalRepository(project, name string) string {
	return localNamespace + naming.PathSeparator + naming.Sanitize(project) + naming.PathSeparator + name
}

func RegistryRepository(project, repository string) string {
	return naming.Sanitize(project) + projectSeparator + naming.RepositorySegment(repository)
}

func FormatRef(project, repository, tag string, target provider.RegistryTarget) string {
	if !target.Named() {
		return LocalRepository(project, naming.RepositorySegment(repository)) + ":" + tag
	}
	return target.ImageRef(RegistryRepository(project, repository), tag)
}
