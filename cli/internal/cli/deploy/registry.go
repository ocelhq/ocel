package deploy

import (
	"context"

	"github.com/ocelhq/ocel/cli/internal/appregistry"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/internal/provider"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

func imageRegistry(ctx context.Context, runner *provider.Runner, cfg *projectconfig.Config, tier environmentv1.Tier) (*contractv1.ImageRegistry, error) {
	if len(cfg.Apps) == 0 {
		return nil, nil
	}
	client, err := runner.Client()
	if err != nil {
		return nil, err
	}
	target, named, err := appregistry.Resolve(ctx, cfg, client, tier)
	if err != nil || !named {
		return nil, err
	}
	return appregistry.Wire(target), nil
}
