package provider

import (
	"context"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
)

type ContainerReleaseHooks struct {
	ScaleUp   func(ctx context.Context, promoted PromotedReleases, progress progress.Log) error
	ScaleDown func(ctx context.Context, demoted DemotedReleases, progress progress.Log) error
}

type AppRelease struct {
	App     string
	Release Release
}

type PromotedReleases struct {
	Slug     string
	Tier     environment.Tier
	Env      string
	Releases []AppRelease
}

type DemotedReleases struct {
	Slug     string
	Tier     environment.Tier
	Env      string
	Previous []AppRelease
	Older    []AppRelease
}
