package commands

import (
	"context"
	"errors"
	"fmt"

	"github.com/ocelhq/ocel/cli/internal/previewid"
	"github.com/ocelhq/ocel/pkg/environment"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
)

func ChooseTier(preview bool) environmentv1.Tier {
	if preview {
		return environmentv1.Tier_TIER_PREVIEW
	}
	return environmentv1.Tier_TIER_PRODUCTION
}

func RefuseEnvironmentWithoutPreview(preview bool, environment string) error {
	if environment == "" || preview {
		return nil
	}
	return errors.New("--environment addresses one preview environment's override, and production has a single environment; pass --preview, or leave --environment off to address the production value")
}

func ResolveDeployedEnvironment(ctx context.Context, cwd, name string, readGitBranch func(ctx context.Context, dir string) (string, error), discoverPRNumber func() string) (*environmentv1.Environment, error) {
	switch name {
	case string(environment.TierProduction):
		return &environmentv1.Environment{Tier: environmentv1.Tier_TIER_PRODUCTION, Lifecycle: environmentv1.Lifecycle_LIFECYCLE_UNSPECIFIED}, nil
	case string(environment.TierPreview):
		return previewid.ResolveEnvironment(ctx, cwd, "", environmentv1.Lifecycle_LIFECYCLE_UNSPECIFIED, readGitBranch, discoverPRNumber)
	default:
		return nil, fmt.Errorf("--env is %q, want %s or %s", name, environment.TierProduction, environment.TierPreview)
	}
}
