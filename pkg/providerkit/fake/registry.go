package fake

import (
	"context"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

func (p *Provider) EnsureImageRegistry(context.Context, providerkit.Class, []string) (providerkit.RegistryTarget, error) {
	return providerkit.RegistryTarget{Server: RegistryServer, Namespace: RegistryNamespace, Username: "fake", Password: "fake-token"}, nil
}

const (
	RegistryServer    = "registry.fake.invalid"
	RegistryNamespace = "ocel"
)
