package deploy

import (
	"fmt"

	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
)

// BuildIDs maps each app name (ManifestApp.name) to the build id its
// Deployment carries this deploy — a Next app's routing-manifest buildId, or
// a host-generated id for a framework with none. The deploy host assigns
// these before planning; BuildPlan only arranges them.
type BuildIDs map[string]string

// Promotion is the project-wide unit one production deploy produces: a
// promotion id grouping the per-app build ids it makes active. Mirrors
// Promotion in workers/deployments-store/src/store.ts — the two must agree
// on shape since the host writes this straight to the deployments store.
type Promotion struct {
	PromotionID string
	Builds      map[string]string // app -> build id
}

// Plan is the tier/stack plan one production deploy realizes: the stable
// infra-tier stack, one app-deploy stack per app, and the Promotion those
// app-deploy stacks' Deployments belong to. The root tier is not part of the
// plan — it is reconciled imperatively through edge.Provider, not a Pulumi
// stack.
type Plan struct {
	InfraStack string
	AppStacks  map[string]string // app -> app-deploy stack name
	Promotion  Promotion
}

// InfraStackName returns the stable, per-project Pulumi stack name for the
// infra tier: SDK-declared resources (postgres, bucket, …). It never varies
// across a project's production deploys — a deploy realizes it in place —
// so unlike an app-deploy stack it carries no build id. Pure.
func InfraStackName(projectID string) string {
	return safeName(projectID) + "--infra"
}

// AppDeployStackName returns the deterministic, per-deploy Pulumi stack name
// for one app's app-deploy tier: unique per (project, app, build id), so
// every deploy of an app gets its own stack instead of mutating the last one
// — the prior stack, and the Deployment it produced, stays live until prune
// reclaims it. Each segment runs through safeName before joining, so no
// segment can itself contain the "--" delimiter (safeName collapses runs of
// "-" to one) — two different (project, app, build id) triples can never
// join into the same name. Pure.
func AppDeployStackName(projectID, app, buildID string) string {
	return safeName(projectID) + "--" + safeName(app) + "--" + safeName(buildID)
}

// BuildPlan turns a manifest, its production environment, a promotion id,
// and the per-app build ids this deploy produced into the tier/stack Plan
// the deploy and prune paths consume. Production-only: tiers and rollback
// don't exist for previews, which keep the single in-place stack model. Pure
// — no Pulumi, AWS, or Cloudflare calls. Every app the manifest declares
// must have an entry in builds, else BuildPlan errors rather than silently
// planning a partial deploy.
func BuildPlan(manifest *deploymentsv1.Manifest, env *deploymentsv1.Environment, promotionID string, builds BuildIDs) (Plan, error) {
	if env.GetClass() != deploymentsv1.Environment_CLASS_PRODUCTION {
		return Plan{}, fmt.Errorf("deploy plan is production-only, got class %s", env.GetClass())
	}

	projectID := manifest.GetProjectId()
	apps := manifest.GetApps()
	plan := Plan{
		InfraStack: InfraStackName(projectID),
		AppStacks:  make(map[string]string, len(apps)),
		Promotion: Promotion{
			PromotionID: promotionID,
			Builds:      make(map[string]string, len(apps)),
		},
	}
	for _, app := range apps {
		name := app.GetName()
		buildID, ok := builds[name]
		if !ok {
			return Plan{}, fmt.Errorf("missing build id for app %q", name)
		}
		plan.AppStacks[name] = AppDeployStackName(projectID, name, buildID)
		plan.Promotion.Builds[name] = buildID
	}
	return plan, nil
}
