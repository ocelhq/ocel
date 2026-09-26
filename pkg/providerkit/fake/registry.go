package fake

import (
	"context"

	"github.com/ocelhq/ocel/pkg/providerkit/images"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func (p *Provider) EnsureImageRegistry(context.Context, edge.Class, []string) (images.RegistryTarget, error) {
	return images.RegistryTarget{Server: RegistryServer, Namespace: RegistryNamespace, Username: "fake", Password: "fake-token"}, nil
}

const (
	RegistryServer    = "registry.fake.invalid"
	RegistryNamespace = "ocel"
)
