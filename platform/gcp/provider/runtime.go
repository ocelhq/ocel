package gcp

import (
	"context"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/platform/gcp/provider/payloads"
)

func (p *Provider) ContainerArch(_ context.Context, app, declared string) (string, error) {
	if runs, _ := providerkit.GoArch(declared); runs != payloads.ContainerArch {
		return "", providerkit.Refuse(providerkit.CodeInvalid,
			"app %s declares arch %q, and Cloud Run runs %s alone: drop the arch, or deploy %s to a provider that runs %s",
			app, declared, providerkit.ArchX8664, app, declared)
	}
	return payloads.ContainerArch, nil
}

func (p *Provider) ContainerRuntime(_ context.Context, arch string) ([]byte, error) {
	return payloads.ContainerRuntime(arch)
}
