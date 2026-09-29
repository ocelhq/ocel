package providerclient

import (
	"context"
	"fmt"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/node"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

func prepareLaunch(ctx context.Context, cfg *projectconfig.Config, pins Pinning) (Config, error) {
	if err := node.Ensure(cfg.Dir); err != nil {
		return Config{}, err
	}
	desc, err := cfg.RequireProvider()
	if err != nil {
		return Config{}, err
	}
	binPath, err := locateProvider(ctx, cfg.Dir, desc.ID, pins)
	if err != nil {
		return Config{}, err
	}
	providerConfig, err := providerConfig(cfg, desc)
	if err != nil {
		return Config{}, err
	}
	return Config{
		BinaryPath:     binPath,
		ProviderConfig: providerConfig,
		ProviderName:   desc.ID,
	}, nil
}

func providerConfig(cfg *projectconfig.Config, desc *projectconfig.ProviderDescriptor) (*contractv1.ProviderConfig, error) {
	config := &contractv1.ProviderConfig{Transforms: cfg.Transforms, Slug: cfg.Slug, ProjectDir: cfg.Dir}
	if desc == nil || len(desc.Options) == 0 {
		return config, nil
	}
	options := &structpb.Struct{}
	if err := protojson.Unmarshal(desc.Options, options); err != nil {
		return nil, fmt.Errorf("the config configures provider %q with options that are not an object: %w", desc.ID, err)
	}
	config.Options = options
	return config, nil
}
