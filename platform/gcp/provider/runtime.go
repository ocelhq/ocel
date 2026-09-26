package gcp

import (
	"context"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/providerkit/refusal"
	"github.com/ocelhq/ocel/platform/gcp/provider/payloads"
)

type containerRuntime struct{ *Provider }

func (p containerRuntime) Arch(_ context.Context, app, declared string) (string, error) {
	if runs, _ := providerkit.GoArch(declared); runs != payloads.ContainerArch {
		return "", refusal.Refuse(refusal.CodeInvalid,
			"app %s declares arch %q, and Cloud Run runs %s alone: drop the arch, or deploy %s to a provider that runs %s",
			app, declared, providerkit.ArchX8664, app, declared)
	}
	return payloads.ContainerArch, nil
}

func (p containerRuntime) Binary(_ context.Context, arch string) ([]byte, error) {
	return payloads.ContainerRuntime(arch)
}
