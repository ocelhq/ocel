package registry

import (
	"strings"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
)

const (
	ecrHostLabels = 6
	ecrHostDocker = "dkr"
	ecrHostECR    = "ecr"
)

func ParseImageRepository(namespace, imageRef string) (name, region string, found bool) {
	server, _, _ := strings.Cut(imageRef, "/")
	labels := strings.Split(server, ".")
	if len(labels) < ecrHostLabels || labels[1] != ecrHostDocker || labels[2] != ecrHostECR {
		return "", "", false
	}
	name, err := repositoryOf(provider.RegistryTarget{Server: server}, imageRef)
	if err != nil || !strings.HasPrefix(name, naming.NamespaceField(namespace)+"/") {
		return "", "", false
	}
	return name, labels[3], true
}
