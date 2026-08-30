package deploy

import (
	"context"

	"github.com/ocelhq/ocel/cli/internal/appimages"
	"github.com/ocelhq/ocel/cli/internal/appregistry"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/internal/provider"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

func imageRegistry(ctx context.Context, runner *provider.Runner, cfg *projectconfig.Config) (*contractv1.ImageRegistry, error) {
	if len(appimages.Apps(cfg)) == 0 {
		return nil, nil
	}
	client, err := runner.Client()
	if err != nil {
		return nil, err
	}
	target, named, err := appregistry.Resolve(ctx, cfg, client)
	if err != nil || !named {
		return nil, err
	}
	return appregistry.Wire(target), nil
}
