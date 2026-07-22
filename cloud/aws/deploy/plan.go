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

// Plan is the stack plan one production deploy realizes: the stable
// infra stack, one app-deploy stack per app, and the Promotion those
// app-deploy stacks' Deployments belong to. The root stack is not part of the
// plan — it is reconciled imperatively through edge.Provider, not a Pulumi
// stack.
type Plan struct {
	InfraStack string
	AppStacks  map[string]string // app -> app-deploy stack name
	Promotion  Promotion
}

// InfraStackName returns the stable, per-project Pulumi stack name for the
// infra stack: SDK-declared resources (postgres, bucket, …). It never varies
// across a project's production deploys — a deploy realizes it in place —
// so unlike an app-deploy stack it carries no build id. Pure.
func InfraStackName(projectID string) string {
	return safeName(projectID) + "--infra"
}

// PreviewInfraStackName returns the per-name Pulumi stack name for a persistent
// preview's infra stack. Each persistent preview (e.g. "staging") gets its own
// isolated db/bucket, so the name incorporates the preview pointer and stays
// distinct from production's infra stack and from every other persistent
// preview. Ephemeral previews have no infra stack. Pure.
func PreviewInfraStackName(projectID, pointer string) string {
	return safeName(projectID) + "--preview-" + safeName(pointer) + "--infra"
}

// PreviewAppDeployStackName returns the per-deploy Pulumi stack name for one
// app's app-deploy stack in a preview: unique per (project, pointer, app, build
// id). The fixed "preview-<pointer>" segment keeps it distinct from any
// production app-deploy stack even in a shared backend. Pure.
func PreviewAppDeployStackName(projectID, pointer, app, buildID string) string {
	return safeName(projectID) + "--preview-" + safeName(pointer) + "--" + safeName(app) + "--" + safeName(buildID)
}

// AppDeployStackName returns the deterministic, per-deploy Pulumi stack name
// for one app's app-deploy stack: unique per (project, app, build id), so
// every deploy of an app gets its own stack instead of mutating the last one
// — the prior stack, and the Deployment it produced, stays live until prune
// reclaims it. Each segment runs through safeName before joining, so no
// segment can itself contain the "--" delimiter (safeName collapses runs of
// "-" to one) — two different (project, app, build id) triples can never
// join into the same name. Pure.
func AppDeployStackName(projectID, app, buildID string) string {
	return safeName(projectID) + "--" + safeName(app) + "--" + safeName(buildID)
}

// BuildPlan turns a manifest, its environment, a promotion id, and the per-app
// build ids into the stack Plan the deploy and prune paths consume. Preview
// stacks are scoped by the environment identity (the store pointer); an
// ephemeral preview gets no infra stack (InfraStack is ""). Every app the
// manifest declares must have an entry in builds, else BuildPlan errors.
func BuildPlan(manifest *deploymentsv1.Manifest, env *deploymentsv1.Environment, promotionID string, builds BuildIDs) (Plan, error) {
	projectID := manifest.GetProjectId()
	apps := manifest.GetApps()

	var (
		infraStack string
		appStack   func(app, buildID string) string
		ephemeral  bool
	)
	switch env.GetClass() {
	case deploymentsv1.Environment_CLASS_PRODUCTION:
		infraStack = InfraStackName(projectID)
		appStack = func(app, buildID string) string {
			return AppDeployStackName(projectID, app, buildID)
		}
	case deploymentsv1.Environment_CLASS_PREVIEW:
		pointer := env.GetIdentity()
		if pointer == "" {
			return Plan{}, fmt.Errorf("preview deploy plan requires an environment identity (the store pointer)")
		}
		ephemeral = env.GetLifecycle() == deploymentsv1.Environment_LIFECYCLE_EPHEMERAL
		// Ephemeral previews get no infra stack; persistent ones get a per-name one.
		if !ephemeral {
			infraStack = PreviewInfraStackName(projectID, pointer)
		}
		appStack = func(app, buildID string) string {
			return PreviewAppDeployStackName(projectID, pointer, app, buildID)
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
		buildID, ok := builds[name]
		if !ok {
			return Plan{}, fmt.Errorf("missing build id for app %q", name)
		}
		plan.AppStacks[name] = appStack(name, buildID)
		plan.Promotion.Builds[name] = buildID
	}
	return plan, nil
}
