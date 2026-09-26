package images

import (
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/providerkit/provider"
)

func Ref(repository, tag string, target provider.RegistryTarget) string {
	if !target.Named() {
		return repository + ":" + tag
	}
	return target.ImageRef(naming.RepositorySegment(repository), tag)
}
