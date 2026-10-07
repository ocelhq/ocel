package deploy

import (
	"context"

	"github.com/ocelhq/ocel/cli/internal/declaration"
	"github.com/ocelhq/ocel/cli/internal/manifest"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/providerprocess"
	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	"github.com/ocelhq/ocel/pkg/provider"
)

type infraProvisioning struct {
	provider       *providerprocess.Provider
	cfg            *project.Project
	env            *environmentv1.Environment
	workerCeilings []provider.WorkerCeiling

	provisioned bool
}

func newInfraProvisioning(p *providerprocess.Provider, cfg *project.Project, env *environmentv1.Environment, workerCeilings []provider.WorkerCeiling, dry, prebuilt bool) *infraProvisioning {
	if dry || prebuilt || env.GetLifecycle() == environmentv1.Lifecycle_LIFECYCLE_EPHEMERAL {
		return nil
	}
	return &infraProvisioning{provider: p, cfg: cfg, env: env, workerCeilings: workerCeilings}
}

func (i *infraProvisioning) provision(ctx context.Context, resources []declaration.Resource, inline []*bindingsv1.Binding) error {
	if i == nil || len(resources) == 0 {
		return nil
	}
	assembled, err := manifest.AssembleInfra(manifest.InfraInput{
		Project:        i.cfg,
		Tier:           i.env.GetTier(),
		Resources:      resources,
		WorkerCeilings: i.workerCeilings,
	})
	if err != nil {
		return err
	}
	if _, err := providerprocess.Stream(ctx, i.provider, "ProvisionInfra", &contractv1.ProvisionInfraRequest{
		Manifest:       assembled,
		Environment:    i.env,
		Edge:           i.cfg.EdgeSelection(),
		InlineBindings: inline,
	}, contractv1connect.ProviderServiceClient.ProvisionInfra); err != nil {
		return err
	}
	i.provisioned = true
	return nil
}

func (i *infraProvisioning) isProvisioned() bool {
	return i != nil && i.provisioned
}
