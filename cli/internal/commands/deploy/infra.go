package deploy

import (
	"context"

	"google.golang.org/protobuf/proto"

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
	providerProcess *providerprocess.Provider
	cfg             *project.Project
	env             *environmentv1.Environment
	aliasToken      string
	workerCeilings  []provider.WorkerCeiling
	provisions      bool
	dry             bool

	sent *contractv1.ProvisionInfraRequest
}

func newInfraProvisioning(providerProcess *providerprocess.Provider, env *environmentv1.Environment, facts preflightFacts, dry, prebuilt bool) *infraProvisioning {
	if prebuilt {
		return nil
	}
	return &infraProvisioning{
		providerProcess: providerProcess,
		cfg:             facts.project,
		env:             env,
		aliasToken:      facts.builtAlias,
		workerCeilings:  facts.workerCeilings,
		provisions:      !dry && env.GetLifecycle() != environmentv1.Lifecycle_LIFECYCLE_EPHEMERAL,
		dry:             dry,
	}
}

func (i *infraProvisioning) provision(ctx context.Context, resources []declaration.Resource, inline []*bindingsv1.Binding) error {
	if i == nil || !i.provisions || len(resources) == 0 {
		return nil
	}
	assembled, err := i.assemble(resources)
	if err != nil {
		return err
	}
	req := &contractv1.ProvisionInfraRequest{
		Manifest:       assembled,
		Environment:    i.env,
		Edge:           i.cfg.EdgeSelection(),
		InlineBindings: inline,
		AliasToken:     i.aliasToken,
	}
	if proto.Equal(req, i.sent) {
		return nil
	}
	if _, err := providerprocess.Stream(ctx, i.providerProcess, "ProvisionInfra", req, contractv1connect.ProviderServiceClient.ProvisionInfra); err != nil {
		return err
	}
	i.sent = req
	return nil
}

func (i *infraProvisioning) assemble(resources []declaration.Resource) (*contractv1.Manifest, error) {
	return manifest.AssembleInfra(manifest.InfraInput{
		Project:        i.cfg,
		Tier:           i.env.GetTier(),
		Resources:      resources,
		WorkerCeilings: i.workerCeilings,
	})
}

func (i *infraProvisioning) isProvisioned() bool {
	return i != nil && i.sent != nil
}
