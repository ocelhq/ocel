package providerprocess

import (
	"context"

	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/pkg/buildoutput"
	costv1 "github.com/ocelhq/ocel/pkg/proto/common/cost/v1"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
)

func (p *Provider) Inventory(ctx context.Context, cfg *project.Project, manifest *contractv1.Manifest, env *environmentv1.Environment) (*costv1.ResourceSet, error) {
	outputRoot, err := buildoutput.Root(cfg.Dir)
	if err != nil {
		return nil, err
	}
	var set *costv1.ResourceSet
	err = p.Call(ctx, func(client contractv1connect.ProviderServiceClient) (err error) {
		set, err = client.Inventory(ctx, &contractv1.InventoryRequest{
			Manifest:    manifest,
			Environment: env,
			Edge:        cfg.EdgeSelection(),
			OutputRoot:  outputRoot,
		})
		return err
	})
	return set, err
}
