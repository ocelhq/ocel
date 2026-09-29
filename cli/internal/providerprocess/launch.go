package providerprocess

import (
	"context"
	"fmt"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/ocelhq/ocel/cli/internal/executables"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/node"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

func newLaunchSpec(ctx context.Context, cfg *project.Project, pinning executables.Pinning) (LaunchSpec, error) {
	if err := node.Ensure(cfg.Dir); err != nil {
		return LaunchSpec{}, err
	}
	declared, err := cfg.RequireProvider()
	if err != nil {
		return LaunchSpec{}, err
	}
	binary, err := executables.EnsureProvider(ctx, cfg.Dir, declared.ID, pinning)
	if err != nil {
		return LaunchSpec{}, err
	}
	providerConfig, err := newProviderConfig(cfg, declared)
	if err != nil {
		return LaunchSpec{}, err
	}
	return LaunchSpec{
		BinaryPath:     binary,
		ProviderConfig: providerConfig,
		ProviderName:   declared.ID,
	}, nil
}

func newProviderConfig(cfg *project.Project, declared *project.Provider) (*contractv1.ProviderConfig, error) {
	config := &contractv1.ProviderConfig{Transforms: cfg.Transforms, Slug: cfg.Slug, ProjectDir: cfg.Dir}
	if declared == nil || len(declared.Options) == 0 {
		return config, nil
	}
	options := &structpb.Struct{}
	if err := protojson.Unmarshal(declared.Options, options); err != nil {
		return nil, fmt.Errorf("the config configures provider %q with options that are not an object: %w", declared.ID, err)
	}
	config.Options = options
	return config, nil
}
