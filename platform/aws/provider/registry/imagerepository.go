package registry

import (
	"strings"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/images"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
)

const (
	ecrHostLabels = 6
	ecrHostDocker = "dkr"
	ecrHostECR    = "ecr"
)

func ParseImageRepository(namespace string, tier environment.Tier, imageRef string) (name, project, region string, found bool) {
	server, _, _ := strings.Cut(imageRef, "/")
	labels := strings.Split(server, ".")
	if len(labels) < ecrHostLabels || labels[1] != ecrHostDocker || labels[2] != ecrHostECR {
		return "", "", "", false
	}
	name, err := repositoryOf(provider.RegistryTarget{Server: server}, imageRef)
	if err != nil {
		return "", "", "", false
	}
	repository, inside := strings.CutPrefix(name, RepositoryPrefix(namespace, tier)+naming.PathSeparator)
	if !inside {
		return "", "", "", false
	}
	project, _ = images.RegistryRepositoryProject(repository)
	return name, project, labels[3], true
}
