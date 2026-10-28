package previewid

import (
	"context"

	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
)

func ResolveEnvironment(ctx context.Context, cwd, name string, lifecycle environmentv1.Lifecycle, readGitBranch func(ctx context.Context, dir string) (string, error), discoverPRNumber func() string) (*environmentv1.Environment, error) {
	if name != "" {
		return &environmentv1.Environment{Tier: environmentv1.Tier_TIER_PREVIEW, Lifecycle: lifecycle, Identity: name}, nil
	}
	branch, err := readGitBranch(ctx, cwd)
	if err != nil {
		return nil, err
	}
	id, err := Resolve(branch, discoverPRNumber())
	if err != nil {
		return nil, err
	}
	return &environmentv1.Environment{Tier: environmentv1.Tier_TIER_PREVIEW, Lifecycle: lifecycle, Identity: id.Key, Label: id.Label}, nil
}
