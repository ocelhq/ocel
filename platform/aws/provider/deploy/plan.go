package deploy

import (
	"fmt"

	"github.com/ocelhq/ocel/pkg/naming"
	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/environment/v1"
)

const (
	ProductionEnv = "prod"
	EveryPreview  = ""
)

type Promotion struct {
	PromotionID string
	Builds      map[string]string
}

type Plan struct {
	InfraStack naming.StackName
	AppStacks  map[string]naming.StackName
	Promotion  Promotion
}

func EnvName(env *environmentv1.Environment) (string, error) {
	switch env.GetTier() {
	case environmentv1.Tier_TIER_PRODUCTION:
		return ProductionEnv, nil
	case environmentv1.Tier_TIER_PREVIEW:
		return previewEnvName(env.GetIdentity())
	default:
		return "", fmt.Errorf("deploys support production and preview, got class %s", env.GetTier())
	}
}

func EnvScope(env *environmentv1.Environment) (string, error) {
	if env.GetTier() == environmentv1.Tier_TIER_PREVIEW && env.GetIdentity() == "" {
		return EveryPreview, nil
	}
	return EnvName(env)
}

func previewEnvName(pointer string) (string, error) {
	if pointer == "" {
		return "", fmt.Errorf("a preview deploy requires an environment identity (the store pointer)")
	}
	if err := naming.Validate("preview name", pointer); err != nil {
		return "", err
	}
	if pointer == ProductionEnv {
		return "", fmt.Errorf("preview name %q is the production environment's name, so this preview would deploy over production; rename the preview", pointer)
	}
	return pointer, nil
}

func releaseOf(id Identity) naming.Release {
	return naming.NewRelease(id.DeploymentID(), id.Fingerprint())
}

func BuildPlan(manifest *deploymentsv1.Manifest, env *environmentv1.Environment, promotionID string, identities Identities) (Plan, error) {
	envName, err := EnvName(env)
	if err != nil {
		return Plan{}, err
	}

	apps := manifest.GetApps()
	plan := Plan{
		AppStacks: make(map[string]naming.StackName, len(apps)),
		Promotion: Promotion{
			PromotionID: promotionID,
			Builds:      make(map[string]string, len(apps)),
		},
	}
	if !ephemeralPreview(env) {
		plan.InfraStack = naming.InfraStack(envName)
	}
	for _, app := range apps {
		name := app.GetName()
		if name == naming.InfraApp {
			return Plan{}, fmt.Errorf("app %q uses the name reserved for the environment's infra stack; rename the app", name)
		}
		id, ok := identities[name]
		if !ok {
			return Plan{}, fmt.Errorf("missing deployment identity for app %q", name)
		}
		plan.AppStacks[name] = naming.AppStack(envName, name, releaseOf(id))
		plan.Promotion.Builds[name] = id.String()
	}
	return plan, nil
}

func ephemeralPreview(env *environmentv1.Environment) bool {
	return env.GetTier() == environmentv1.Tier_TIER_PREVIEW &&
		env.GetLifecycle() == environmentv1.Lifecycle_LIFECYCLE_EPHEMERAL
}
