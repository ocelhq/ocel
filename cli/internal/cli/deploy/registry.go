package deploy

import (
	"context"

	"github.com/ocelhq/ocel/cli/internal/appregistry"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/internal/providerclient"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	"github.com/ocelhq/ocel/pkg/provider"
)

func imageRegistry(ctx context.Context, prov *providerclient.Provider, cfg *projectconfig.Config, tier environmentv1.Tier) (*contractv1.ImageRegistry, error) {
	if len(cfg.Apps) == 0 {
		return nil, nil
	}
	var target provider.RegistryTarget
	var named bool
	err := prov.Call(ctx, func(client contractv1connect.ProviderServiceClient) error {
		var err error
		target, named, err = appregistry.Resolve(ctx, cfg, client, tier)
		return err
	})
	if err != nil || !named {
		return nil, err
	}
	return appregistry.Wire(target), nil
}
