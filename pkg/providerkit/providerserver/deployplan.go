package providerserver

import (
	"slices"

	"github.com/ocelhq/ocel/pkg/naming"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	"github.com/ocelhq/ocel/pkg/providerkit/refusal"
	"github.com/ocelhq/ocel/pkg/providerkit/stackrecords"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const EveryPreview = ""

func buildDeployPlan(req *contractv1.DeployRequest, promotionID string) (provider.DeployPlan, error) {
	manifest := req.GetManifest()
	env := req.GetEnvironment()

	class, err := classOf(env.GetTier())
	if err != nil {
		return provider.DeployPlan{}, err
	}
	name, err := envName(env)
	if err != nil {
		return provider.DeployPlan{}, err
	}
	slug := manifest.GetSlug()
	if slug == "" {
		return provider.DeployPlan{}, refusal.Refuse(refusal.CodeInvalid, "this manifest names no project, and every stack a deploy stands up belongs to one")
	}

	plan := provider.DeployPlan{
		Slug:        slug,
		Class:       class,
		Env:         name,
		Label:       env.GetLabel(),
		Pointer:     pointerFor(class, name),
		PromotionID: promotionID,
		Tag:         req.GetTag(),
		Builds:      make(map[string]string, len(manifest.GetApps())),
	}
	if !ephemeral(env) {
		plan.Infra = naming.InfraStack(name)
	}
	containers, err := appContainers(manifest)
	if err != nil {
		return provider.DeployPlan{}, err
	}
	for _, app := range manifest.GetApps() {
		entry, err := appEntry(app, name)
		if err != nil {
			return provider.DeployPlan{}, err
		}
		if container, ours := containers[entry.App]; ours {
			entry.Image = container.GetImage()
			entry.HealthCheckPath = container.GetHealthCheckPath()
			entry.Arch = container.GetArch()
			plan.Builds[entry.App] = entry.Image
		} else {
			plan.Builds[entry.App] = entry.Build.String()
		}
		plan.Apps = append(plan.Apps, entry)
	}
	if err := refuseOrphanFunctions(manifest, plan.Builds); err != nil {
		return provider.DeployPlan{}, err
	}
	return plan, nil
}

func appContainers(manifest *contractv1.Manifest) (map[string]*contractv1.ManifestContainer, error) {
	compute := make(map[string]string, len(manifest.GetApps()))
	for _, app := range manifest.GetApps() {
		compute[app.GetName()] = app.GetCompute()
	}

	containers := make(map[string]*contractv1.ManifestContainer, len(manifest.GetContainers()))
	for _, container := range manifest.GetContainers() {
		app := container.GetApp()
		kind, declared := compute[app]
		if !declared {
			return nil, refusal.Refuse(refusal.CodeInvalid,
				"a container names the app %q, which this manifest does not declare", app)
		}
		if kind != string(provider.ComputeContainer) {
			return nil, refusal.Refuse(refusal.CodeInvalid,
				"a container names the app %q, which this manifest says runs on %q compute", app, kind)
		}
		if _, twice := containers[app]; twice {
			return nil, refusal.Refuse(refusal.CodeInvalid,
				"app %q carries two containers, and an app is served by one process", app)
		}
		containers[app] = container
	}
	for app, kind := range compute {
		if kind == string(provider.ComputeContainer) && containers[app].GetImage() == "" {
			return nil, refusal.Refuse(refusal.CodeInvalid,
				"app %q runs on container compute and this manifest carries no image for it", app)
		}
	}
	for _, fn := range manifest.GetFunctions() {
		if _, served := containers[fn.GetApp()]; served {
			return nil, refusal.Refuse(refusal.CodeInvalid,
				"app %q runs on container compute and this manifest packs function %s into it as well, so two things would answer the same request",
				fn.GetApp(), fn.GetLogicalName())
		}
	}
	return containers, nil
}

func refuseOrphanFunctions(manifest *contractv1.Manifest, declared map[string]string) error {
	for _, fn := range manifest.GetFunctions() {
		app := fn.GetApp()
		if app == "" {
			return refusal.Refuse(refusal.CodeInvalid,
				"function %s names no app, and a function ships inside the app that declares it", fn.GetLogicalName())
		}
		if _, ours := declared[app]; !ours {
			return refusal.Refuse(refusal.CodeInvalid,
				"function %s names the app %q, which this manifest does not declare", fn.GetLogicalName(), app)
		}
	}
	return nil
}

func appEntry(app *contractv1.ManifestApp, env string) (provider.AppEntry, error) {
	name := app.GetName()
	if name == "" {
		return provider.AppEntry{}, refusal.Refuse(refusal.CodeInvalid, "this manifest carries an app with no name, and a stack is named after the app it serves")
	}
	if name == naming.InfraApp {
		return provider.AppEntry{}, refusal.Refuse(refusal.CodeInvalid,
			"app %q uses the name reserved for the environment's infra stack; rename the app", name)
	}
	if err := naming.Validate("app name", name); err != nil {
		return provider.AppEntry{}, refusal.Refuse(refusal.CodeInvalid, "%s", err.Error())
	}
	identity, err := provider.NewBuild(app.GetDeploymentId(), env, provider.FingerprintVariables(app.GetVariables()))
	if err != nil {
		return provider.AppEntry{}, err
	}
	return provider.AppEntry{
		App:      name,
		Stack:    naming.AppStack(env, name, identity.Release()),
		Build:    identity,
		Manifest: app,
	}, nil
}

func pointerFor(class edge.Class, env string) string {
	if class == edge.ClassProduction {
		return edge.DefaultPointer
	}
	return env
}

func ephemeral(env *environmentv1.Environment) bool {
	return env.GetTier() == environmentv1.Tier_TIER_PREVIEW &&
		env.GetLifecycle() == environmentv1.Lifecycle_LIFECYCLE_EPHEMERAL
}

func envScope(env *environmentv1.Environment) (string, error) {
	if env.GetTier() == environmentv1.Tier_TIER_PREVIEW && env.GetIdentity() == "" {
		return EveryPreview, nil
	}
	return envName(env)
}

func bindingEnvironment(p provider.DeployPlan) string {
	if p.Class == edge.ClassProduction {
		return ""
	}
	return p.Env
}

func appCoordinate(p provider.DeployPlan, app string, release naming.Release) naming.Coordinate {
	return naming.Coordinate{
		Project: naming.Sanitize(p.Slug),
		Env:     p.Env,
		App:     app,
		Release: release,
	}
}

func appTags(p provider.DeployPlan, entry provider.AppEntry) map[string]string {
	coordinate := appCoordinate(p, entry.App, entry.Build.Release())
	coordinate.Kind = naming.KindFunction
	return coordinate.Tags(naming.Facts{
		ManagedBy:  "ocel",
		EnvClass:   string(p.Class),
		Deployment: entry.Build.DeploymentID(),
		Promotion:  p.PromotionID,
	})
}

func infraTags(p provider.DeployPlan) map[string]string {
	tags := map[string]string{
		"ocel:managed-by": "ocel",
		"ocel:project":    naming.Sanitize(p.Slug),
		"ocel:env":        p.Env,
		"ocel:env-class":  string(p.Class),
		"ocel:stack":      p.Infra.String(),
	}
	return tags
}

func classifyStacks(entries []stackrecords.NamedStack, class edge.Class) (infra, apps []naming.StackName, pointers []string) {
	for _, entry := range entries {
		production := entry.Name.Env == stackrecords.ProductionEnv
		if production != (class == edge.ClassProduction) {
			continue
		}
		if !slices.Contains(pointers, entry.Name.Env) {
			pointers = append(pointers, entry.Name.Env)
		}
		if entry.Name.IsInfra() {
			infra = append(infra, entry.Name)
			continue
		}
		apps = append(apps, entry.Name)
	}
	slices.Sort(pointers)
	return infra, apps, pointers
}
