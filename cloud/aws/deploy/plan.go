package deploy

import (
	"fmt"

	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
)

// Promotion is the project-wide unit one production deploy produces: a
// promotion id grouping the per-app Deployment identities it makes active.
// Mirrors Promotion in workers/deployments-store/src/store.ts — the two must
// agree on shape since the host writes this straight to the deployments store.
type Promotion struct {
	PromotionID string
	Builds      map[string]string // app -> rendered Deployment identity
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
func InfraStackName(slug string) string {
	return safeName(slug) + "--infra"
}

// PreviewInfraStackName returns the per-name Pulumi stack name for a persistent
// preview's infra stack. Each persistent preview (e.g. "staging") gets its own
// isolated db/bucket, so the name incorporates the preview pointer and stays
// distinct from production's infra stack and from every other persistent
// preview. Ephemeral previews have no infra stack. Pure.
func PreviewInfraStackName(slug, pointer string) string {
	return safeName(slug) + "--preview-" + safeName(pointer) + "--infra"
}

// PreviewAppDeployStackName returns the per-deploy Pulumi stack name for one
// app's app-deploy stack in a preview: unique per (project, pointer, app,
// Deployment identity). The fixed "preview-<pointer>" segment keeps it distinct
// from any production app-deploy stack even in a shared backend. Pure.
func PreviewAppDeployStackName(slug, pointer, app string, id DeploymentIdentity) string {
	return safeName(slug) + "--preview-" + safeName(pointer) + "--" + safeName(app) + identityNameSegments(id)
}

// AppDeployStackName returns the deterministic, per-deploy Pulumi stack name
// for one app's app-deploy stack: unique per (project, app, Deployment
// identity), so every deploy of an app gets its own stack instead of mutating
// the last one — the prior stack, and the Deployment it produced, stays live
// until prune reclaims it. Each segment runs through safeName before joining, so
// no segment can itself contain the "--" delimiter (safeName collapses runs of
// "-" to one) — two different (project, app, identity) triples can never join
// into the same name. Pure.
func AppDeployStackName(slug, app string, id DeploymentIdentity) string {
	return safeName(slug) + "--" + safeName(app) + identityNameSegments(id)
}

// identityNameSegments is the identity's tail of an app-deploy stack name: the
// build id, plus the value fingerprint as a segment of its own when there is
// one. A fingerprint-free identity therefore names exactly the stack the build
// id alone named, and a fingerprint can never blur into a build id that carries
// the delimiter itself. Pure.
func identityNameSegments(id DeploymentIdentity) string {
	name := "--" + safeName(id.BuildID)
	if id.Fingerprint != "" {
		name += "--" + safeName(id.Fingerprint)
	}
	return name
}

// BuildPlan turns a manifest, its environment, a promotion id, and the per-app
// Deployment identities into the stack Plan the deploy and prune paths consume.
// Preview stacks are scoped by the environment identity (the store pointer); an
// ephemeral preview gets no infra stack (InfraStack is ""). Every app the
// manifest declares must have an entry in identities, else BuildPlan errors.
func BuildPlan(manifest *deploymentsv1.Manifest, env *deploymentsv1.Environment, promotionID string, identities DeploymentIdentities) (Plan, error) {
	slug := manifest.GetSlug()
	apps := manifest.GetApps()

	var (
		infraStack string
		appStack   func(app string, id DeploymentIdentity) string
		ephemeral  bool
	)
	switch env.GetClass() {
	case deploymentsv1.Environment_CLASS_PRODUCTION:
		infraStack = InfraStackName(slug)
		appStack = func(app string, id DeploymentIdentity) string {
			return AppDeployStackName(slug, app, id)
		}
	case deploymentsv1.Environment_CLASS_PREVIEW:
		pointer := env.GetIdentity()
		if pointer == "" {
			return Plan{}, fmt.Errorf("preview deploy plan requires an environment identity (the store pointer)")
		}
		ephemeral = env.GetLifecycle() == deploymentsv1.Environment_LIFECYCLE_EPHEMERAL
		// Ephemeral previews get no infra stack; persistent ones get a per-name one.
		if !ephemeral {
			infraStack = PreviewInfraStackName(slug, pointer)
		}
		appStack = func(app string, id DeploymentIdentity) string {
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
