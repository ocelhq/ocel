package deploy

import (
	"fmt"

	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
)

type Promotion struct {
	PromotionID string
	Builds      map[string]string
}

type Plan struct {
	InfraStack string
	AppStacks  map[string]string
	Promotion  Promotion
}

func InfraStackName(slug string) string {
	return safeName(slug) + "--infra"
}

func PreviewInfraStackName(slug, pointer string) string {
	return safeName(slug) + "--preview-" + safeName(pointer) + "--infra"
}

func PreviewAppDeployStackName(slug, pointer, app string, id Identity) string {
	return safeName(slug) + "--preview-" + safeName(pointer) + "--" + safeName(app) + identityNameSegments(id)
}

func AppDeployStackName(slug, app string, id Identity) string {
	return safeName(slug) + "--" + safeName(app) + identityNameSegments(id)
}

func identityNameSegments(id Identity) string {
	name := "--" + safeName(id.BuildID())
	if id.Fingerprint() != "" {
		name += "--" + safeName(id.Fingerprint())
	}
	return name
}

func BuildPlan(manifest *deploymentsv1.Manifest, env *deploymentsv1.Environment, promotionID string, identities Identities) (Plan, error) {
	slug := manifest.GetSlug()
	apps := manifest.GetApps()

	var (
		infraStack string
		appStack   func(app string, id Identity) string
		ephemeral  bool
	)
	switch env.GetClass() {
	case deploymentsv1.Environment_CLASS_PRODUCTION:
		infraStack = InfraStackName(slug)
		appStack = func(app string, id Identity) string {
			return AppDeployStackName(slug, app, id)
		}
	case deploymentsv1.Environment_CLASS_PREVIEW:
		pointer := env.GetIdentity()
		if pointer == "" {
			return Plan{}, fmt.Errorf("preview deploy plan requires an environment identity (the store pointer)")
		}
		ephemeral = env.GetLifecycle() == deploymentsv1.Environment_LIFECYCLE_EPHEMERAL
		if !ephemeral {
			infraStack = PreviewInfraStackName(slug, pointer)
		}
		appStack = func(app string, id Identity) string {
			return PreviewAppDeployStackName(slug, pointer, app, id)
		}
	default:
		return Plan{}, fmt.Errorf("deploy plan supports production and preview, got class %s", env.GetClass())
	}

	plan := Plan{
		InfraStack: infraStack,
		AppStacks:  make(map[string]string, len(apps)),
		Promotion: Promotion{
			PromotionID: promotionID,
			Builds:      make(map[string]string, len(apps)),
		},
	}
	for _, app := range apps {
		name := app.GetName()
		id, ok := identities[name]
		if !ok {
			return Plan{}, fmt.Errorf("missing deployment identity for app %q", name)
		}
		plan.AppStacks[name] = appStack(name, id)
		plan.Promotion.Builds[name] = id.String()
	}
	return plan, nil
}
