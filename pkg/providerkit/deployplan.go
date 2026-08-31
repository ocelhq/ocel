package providerkit

import (
	"slices"
	"strings"

	"github.com/ocelhq/ocel/pkg/naming"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const EveryPreview = ""

type DeployPlan struct {
	Slug    string
	Class   Class
	Env     string
	Label   string
	Pointer string

	Infra naming.StackName
	Apps  []AppEntry

	PromotionID string
	Tag         string
	Builds      map[string]string
}

type AppEntry struct {
	App      string
	Stack    naming.StackName
	Build    Build
	Manifest *contractv1.ManifestApp

	Image           string
	HealthCheckPath string
}

func (e AppEntry) Compute() Compute { return Compute(e.Manifest.GetCompute()) }

func buildDeployPlan(req *contractv1.DeployRequest, promotionID string) (DeployPlan, error) {
	manifest := req.GetManifest()
	env := req.GetEnvironment()

	class, err := classOf(env.GetTier())
	if err != nil {
		return DeployPlan{}, err
	}
	name, err := envName(env)
	if err != nil {
		return DeployPlan{}, err
	}
	slug := manifest.GetSlug()
	if slug == "" {
		return DeployPlan{}, Refuse(CodeInvalid, "this manifest names no project, and every stack a deploy stands up belongs to one")
	}

	plan := DeployPlan{
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
		return DeployPlan{}, err
	}
	for _, app := range manifest.GetApps() {
		entry, err := appEntry(app, name)
		if err != nil {
			return DeployPlan{}, err
		}
		if container, ours := containers[entry.App]; ours {
			entry.Image = container.GetImage()
			entry.HealthCheckPath = container.GetHealthCheckPath()
			plan.Builds[entry.App] = entry.Image
		} else {
			plan.Builds[entry.App] = entry.Build.String()
		}
		plan.Apps = append(plan.Apps, entry)
	}
	if err := refuseOrphanFunctions(manifest, plan.Builds); err != nil {
		return DeployPlan{}, err
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
			return nil, Refuse(CodeInvalid,
				"a container names the app %q, which this manifest does not declare", app)
		}
		if kind != string(ComputeContainer) {
			return nil, Refuse(CodeInvalid,
				"a container names the app %q, which this manifest says runs on %q compute", app, kind)
		}
		if _, twice := containers[app]; twice {
			return nil, Refuse(CodeInvalid,
				"app %q carries two containers, and an app is served by one process", app)
		}
		containers[app] = container
	}
	for app, kind := range compute {
		if kind == string(ComputeContainer) && containers[app].GetImage() == "" {
			return nil, Refuse(CodeInvalid,
				"app %q runs on container compute and this manifest carries no image for it", app)
		}
	}
	for _, fn := range manifest.GetFunctions() {
		if _, served := containers[fn.GetApp()]; served {
			return nil, Refuse(CodeInvalid,
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
			return Refuse(CodeInvalid,
				"function %s names no app, and a function ships inside the app that declares it", fn.GetLogicalName())
		}
		if _, ours := declared[app]; !ours {
			return Refuse(CodeInvalid,
				"function %s names the app %q, which this manifest does not declare", fn.GetLogicalName(), app)
		}
	}
	return nil
}

func appEntry(app *contractv1.ManifestApp, env string) (AppEntry, error) {
	name := app.GetName()
	if name == "" {
		return AppEntry{}, Refuse(CodeInvalid, "this manifest carries an app with no name, and a stack is named after the app it serves")
	}
	if name == naming.InfraApp {
		return AppEntry{}, Refuse(CodeInvalid,
			"app %q uses the name reserved for the environment's infra stack; rename the app", name)
	}
	if err := naming.Validate("app name", name); err != nil {
		return AppEntry{}, Refuse(CodeInvalid, "%s", err.Error())
	}
	identity, err := NewBuild(app.GetDeploymentId(), env, FingerprintVariables(app.GetVariables()))
	if err != nil {
		return AppEntry{}, err
	}
	return AppEntry{
		App:      name,
		Stack:    naming.AppStack(env, name, identity.Release()),
		Build:    identity,
		Manifest: app,
	}, nil
}

func pointerFor(class Class, env string) string {
	if class == ClassProduction {
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

func (p DeployPlan) linkEnvironment() string {
	if p.Class == ClassProduction {
		return ""
	}
	return p.Env
}

func (p DeployPlan) coordinate(app string, release naming.Release) naming.Coordinate {
	return naming.Coordinate{
		Project: naming.Sanitize(p.Slug),
		Env:     p.Env,
		App:     app,
		Release: release,
	}
}

func (p DeployPlan) tags(entry AppEntry) map[string]string {
	coordinate := p.coordinate(entry.App, entry.Build.Release())
	coordinate.Kind = naming.KindFunction
	return coordinate.Tags(naming.Facts{
		ManagedBy:  "ocel",
		EnvClass:   string(p.Class),
		Deployment: entry.Build.DeploymentID(),
		Promotion:  p.PromotionID,
	})
}

func (p DeployPlan) infraTags() map[string]string {
	tags := map[string]string{
		"ocel:managed-by": "ocel",
		"ocel:project":    naming.Sanitize(p.Slug),
		"ocel:env":        p.Env,
		"ocel:env-class":  string(p.Class),
		"ocel:stack":      p.Infra.String(),
	}
	return tags
}

type ReclaimTarget struct {
	App      string
	Build    Build
	Stack    naming.StackName
	Prefixes []string
}

func ReclaimTargets(slug, env string, removed, surviving, servingHere []string) ([]ReclaimTarget, error) {
	if len(removed) == 0 {
		return nil, nil
	}
	elsewhere := releasesOf(surviving)
	here := releasesOf(servingHere)

	targets := make([]ReclaimTarget, 0, len(removed))
	for _, key := range removed {
		app, identity, ok := splitRecordKey(key)
		if !ok {
			return nil, Refuse(CodeInvalid, "malformed removed record key %q, want %q", key, recordKeyPrefix+"app/identity")
		}
		release := identity.Release()
		targets = append(targets, ReclaimTarget{
			App:      app,
			Build:    identity,
			Stack:    naming.AppStack(env, app, release),
			Prefixes: reclaimedPrefixes(slug, env, app, release, elsewhere, here),
		})
	}
	return targets, nil
}

func reclaimedPrefixes(slug, env, app string, release naming.Release, elsewhere, here map[appRelease]bool) []string {
	coordinate := naming.Coordinate{Project: naming.Sanitize(slug), Env: env, App: app, Release: release}
	released := appRelease{app: app, release: release.String()}
	switch {
	case !elsewhere[released]:
		return []string{coordinate.StoragePrefix()}
	case !here[released]:
		return []string{coordinate.ISRPrefix()}
	}
	return nil
}

const recordKeyPrefix = "record:"

type appRelease struct {
	app     string
	release string
}

func splitRecordKey(key string) (string, Build, bool) {
	app, rendered, split := strings.Cut(strings.TrimPrefix(key, recordKeyPrefix), "/")
	if !split || app == "" {
		return "", Build{}, false
	}
	identity, err := ParseBuild(rendered)
	if err != nil {
		return "", Build{}, false
	}
	return app, identity, true
}

func releasesOf(keys []string) map[appRelease]bool {
	served := make(map[appRelease]bool, len(keys))
	for _, key := range keys {
		app, identity, ok := splitRecordKey(key)
		if !ok {
			continue
		}
		served[appRelease{app: app, release: identity.Release().String()}] = true
	}
	return served
}

func classifyStacks(entries []StackEntry, class Class) (infra, apps []naming.StackName, pointers []string) {
	for _, entry := range entries {
		production := entry.Name.Env == ProductionEnv
		if production != (class == ClassProduction) {
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
