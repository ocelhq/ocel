package fake

import (
	"context"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
)

func (p *Provider) EnsureImageRegistry(context.Context, environment.Tier, []string) (provider.RegistryTarget, error) {
	return provider.RegistryTarget{Server: RegistryServer, Namespace: RegistryNamespace, Username: "fake", Password: "fake-token"}, nil
}

const (
	RegistryServer    = "registry.fake.invalid"
	RegistryNamespace = "ocel"
)
