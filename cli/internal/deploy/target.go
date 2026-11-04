package deploy

import (
	"context"

	"github.com/ocelhq/ocel/cli/internal/previewid"
	"github.com/ocelhq/ocel/cli/internal/telemetry"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	consolev1 "github.com/ocelhq/ocel/pkg/proto/console/v1"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

type Target struct {
	tier       environmentv1.Tier
	tag        string
	name       string
	persistent bool
}

func Production(tag string) Target {
	return Target{tier: environmentv1.Tier_TIER_PRODUCTION, tag: tag}
}

func Preview(name string, persistent bool) Target {
	return Target{tier: environmentv1.Tier_TIER_PREVIEW, name: name, persistent: persistent}
}

func (t Target) isPreview() bool {
	return t.tier == environmentv1.Tier_TIER_PREVIEW
}

func (t Target) command() string {
	if t.isPreview() {
		return "ocel preview up"
	}
	return "ocel deploy"
}

func (t Target) kind() consolev1.DeploymentKind {
	if t.isPreview() {
		return consolev1.DeploymentKind_DEPLOYMENT_KIND_PREVIEW_UP
	}
	return consolev1.DeploymentKind_DEPLOYMENT_KIND_DEPLOY
}

func (t Target) telemetryTarget() string {
	if t.isPreview() {
		return telemetry.DeployTargetPreview
	}
	return telemetry.DeployTargetProduction
}

func (t Target) environment(ctx context.Context, dependencies Dependencies, cwd string) (*environmentv1.Environment, error) {
	if !t.isPreview() {
		return &environmentv1.Environment{Tier: environmentv1.Tier_TIER_PRODUCTION, Lifecycle: environmentv1.Lifecycle_LIFECYCLE_UNSPECIFIED}, nil
	}
	lifecycle := environmentv1.Lifecycle_LIFECYCLE_EPHEMERAL
	if t.persistent {
		lifecycle = environmentv1.Lifecycle_LIFECYCLE_PERSISTENT
	}
	return resolvePreviewEnvironment(ctx, dependencies, cwd, t.name, lifecycle)
}

func place(env *environmentv1.Environment) string {
	if env.GetTier() == environmentv1.Tier_TIER_PREVIEW {
		return "preview " + env.GetIdentity()
	}
	return stackrecords.ProductionEnv
}

func resolvePreviewEnvironment(ctx context.Context, dependencies Dependencies, cwd, name string, lifecycle environmentv1.Lifecycle) (*environmentv1.Environment, error) {
	if name != "" {
		if err := previewid.ValidateLabel(name); err != nil {
			return nil, err
		}
	}
	env, err := previewid.ResolveEnvironment(ctx, cwd, name, lifecycle, dependencies.ReadGitBranch, dependencies.DiscoverPRNumber)
	if job := dependencies.Job(); err == nil && name != "" && job != nil {
		env.Label = job.PreviewLabel
	}
	return env, err
}
